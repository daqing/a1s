package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/client"
	containers "github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/containerd/typeurl/v2"
	"github.com/distribution/reference"

	"github.com/daqing/a1s/app/models"
)

// withSpecForPlatform generates the OCI spec for an explicit platform,
// unlike client.WithNewSpec which always uses the client's own GOOS.
func withSpecForPlatform(platform string, opts ...oci.SpecOpts) client.NewContainerOpts {
	return func(ctx context.Context, c *client.Client, ctr *containers.Container) error {
		// mirror client.WithNewSpec: spec generation resolves against the
		// namespace in the context, not just the gRPC header
		if _, ok := namespaces.Namespace(ctx); !ok {
			ctx = namespaces.WithNamespace(ctx, c.DefaultNamespace())
		}

		spec, err := oci.GenerateSpecWithPlatform(ctx, c, platform, ctr, opts...)
		if err != nil {
			return err
		}

		ctr.Spec, err = typeurl.MarshalAny(spec)
		return err
	}
}

// containerRuntime owns the containerd connection and executes commands
// against it.
type containerRuntime struct {
	addr        string
	namespace   string
	snapshotter string
	platform    string // client-side pull platform override; empty = client default

	mu sync.Mutex
	cd *client.Client
}

// newContainerRuntime builds the runtime from the A1S_CONTAINERD_* environment.
func newContainerRuntime() *containerRuntime {
	return &containerRuntime{
		addr:        envOr("A1S_CONTAINERD_ADDR", "/run/containerd/containerd.sock"),
		namespace:   envOr("A1S_CONTAINERD_NAMESPACE", "a1s"),
		snapshotter: envOr("A1S_CONTAINERD_SNAPSHOTTER", "overlayfs"),
		platform:    os.Getenv("A1S_CONTAINERD_PLATFORM"),
	}
}

// cdClient lazily dials containerd; a failed dial is retried on the next
// command so the worker can start before containerd is up.
func (rt *containerRuntime) cdClient(ctx context.Context) (*client.Client, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if rt.cd != nil {
		return rt.cd, nil
	}

	cd, err := Dial(ctx, rt.addr, rt.namespace)
	if err != nil {
		return nil, err
	}

	rt.cd = cd
	return cd, nil
}

// execute dispatches one command to its action executor.
func (rt *containerRuntime) execute(ctx context.Context, cmd command) commandResult {
	switch cmd.Action {
	case "start":
		return rt.start(ctx, cmd)
	case "stop":
		return rt.stop(ctx, cmd)
	case "remove":
		return rt.remove(ctx, cmd)
	case "inspect":
		return rt.inspect(ctx, cmd)
	default:
		return commandResult{OK: false, Error: fmt.Sprintf("unknown action %q", cmd.Action)}
	}
}

type namePayload struct {
	Name string `json:"name"`
}

func decodeNamePayload(cmd command) (string, error) {
	var payload namePayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return "", fmt.Errorf("decode payload: %v", err)
	}

	if payload.Name == "" {
		return "", fmt.Errorf("payload needs a name")
	}

	return payload.Name, nil
}

// stop kills the container's task (SIGTERM, then SIGKILL after a grace
// period) and deletes the task record; the container object survives in
// stopped state. Stopping something already stopped or gone succeeds.
func (rt *containerRuntime) stop(ctx context.Context, cmd command) commandResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	name, err := decodeNamePayload(cmd)
	if err != nil {
		return commandResult{OK: false, Error: err.Error()}
	}

	cd, err := rt.cdClient(ctx)
	if err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("containerd unavailable: %v", err)}
	}

	container, err := cd.LoadContainer(ctx, name)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return okState("stopped")
		}

		return commandResult{OK: false, Error: fmt.Sprintf("load container: %v", err)}
	}

	task, err := container.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return okState("stopped")
		}

		return commandResult{OK: false, Error: fmt.Sprintf("load task: %v", err)}
	}

	if err := rt.killTask(ctx, task); err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("stop task: %v", err)}
	}

	return okState("stopped")
}

// killTask terminates a task gracefully and reaps it.
func (rt *containerRuntime) killTask(ctx context.Context, task client.Task) error {
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	exitCh, _ := task.Wait(waitCtx)

	reap := func() error {
		_, err := task.Delete(ctx, client.WithProcessKill)
		return err
	}

	if err := task.Kill(ctx, syscall.SIGTERM); err != nil {
		// an exited or never-started task cannot take a signal; force-reap it
		return reap()
	}

	select {
	case <-exitCh:
		_, err := task.Delete(ctx)
		return err
	case <-time.After(5 * time.Second):
		if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
			return reap()
		}

		<-exitCh
		_, err := task.Delete(ctx)
		return err
	case <-waitCtx.Done():
		return reap()
	}
}

// remove kills and deletes the task, then deletes the container object;
// removing something already gone succeeds.
func (rt *containerRuntime) remove(ctx context.Context, cmd command) commandResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	name, err := decodeNamePayload(cmd)
	if err != nil {
		return commandResult{OK: false, Error: err.Error()}
	}

	cd, err := rt.cdClient(ctx)
	if err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("containerd unavailable: %v", err)}
	}

	if err := rt.removeExisting(ctx, cd, name); err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("remove container: %v", err)}
	}

	return okState("removed")
}

// inspect reports the container's mapped status plus the raw containerd
// task state (see docs/state-model.md for the mapping).
func (rt *containerRuntime) inspect(ctx context.Context, cmd command) commandResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	name, err := decodeNamePayload(cmd)
	if err != nil {
		return commandResult{OK: false, Error: err.Error()}
	}

	cd, err := rt.cdClient(ctx)
	if err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("containerd unavailable: %v", err)}
	}

	container, err := cd.LoadContainer(ctx, name)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return okDetail(map[string]any{"exists": false})
		}

		return commandResult{OK: false, Error: fmt.Sprintf("load container: %v", err)}
	}

	detail := map[string]any{"exists": true}

	task, err := container.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			detail["status"] = models.ContainerStopped
			detail["containerd"] = "no_task"
			return okDetail(detail)
		}

		return commandResult{OK: false, Error: fmt.Sprintf("load task: %v", err)}
	}

	status, err := task.Status(ctx)
	if err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("task status: %v", err)}
	}

	detail["status"] = mapTaskStatus(strings.ToUpper(string(status.Status)), status.ExitStatus)
	detail["containerd"] = status.Status
	detail["exit_code"] = status.ExitStatus

	return okDetail(detail)
}

// mapTaskStatus maps a containerd task state onto the container statuses
// from docs/state-model.md. Task states arrive lowercase via the API
// ("running"); ctr displays them uppercase, so the match normalizes case.
func mapTaskStatus(state string, exitCode uint32) string {
	switch strings.ToUpper(state) {
	case "RUNNING":
		return models.ContainerRunning
	case "STOPPED":
		if exitCode == 0 {
			return models.ContainerStopped
		}

		return models.ContainerFailed
	default:
		// CREATED and PAUSED are not-running states A1s does not model
		// separately
		return models.ContainerStopped
	}
}

func okState(state string) commandResult {
	detail, _ := json.Marshal(map[string]any{"state": state})
	return commandResult{OK: true, Detail: detail}
}

func okDetail(detail map[string]any) commandResult {
	encoded, _ := json.Marshal(detail)
	return commandResult{OK: true, Detail: encoded}
}

type startPayload struct {
	// ID is the a1s containers.id; the worker labels the containerd
	// container with it so the status report loop can address the row
	ID      int64             `json:"id"`
	Image   string            `json:"image"`
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// containerIDLabel ties a containerd container back to its a1s row.
const containerIDLabel = "a1s.container.id"

// start pulls the image (when missing), creates the container and starts
// its task. Pulls can take minutes, so the context allows ten minutes
// regardless of the caller's deadline.
func (rt *containerRuntime) start(ctx context.Context, cmd command) commandResult {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()

	var payload startPayload
	if err := json.Unmarshal(cmd.Payload, &payload); err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("decode start payload: %v", err)}
	}
	if payload.Image == "" || payload.Name == "" {
		return commandResult{OK: false, Error: "start payload needs image and name"}
	}

	cd, err := rt.cdClient(ctx)
	if err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("containerd unavailable: %v", err)}
	}

	image, err := rt.ensureImage(ctx, cd, payload.Image)
	if err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("pull %s: %v", payload.Image, err)}
	}

	if err := rt.removeExisting(ctx, cd, payload.Name); err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("clean up old container %s: %v", payload.Name, err)}
	}

	specOpts := []oci.SpecOpts{oci.WithImageConfig(image)}
	if payload.Command != "" {
		specOpts = append(specOpts, oci.WithProcessArgs(append([]string{payload.Command}, payload.Args...)...))
	}
	if len(payload.Env) > 0 {
		pairs := make([]string, 0, len(payload.Env))
		for key, value := range payload.Env {
			pairs = append(pairs, key+"="+value)
		}
		specOpts = append(specOpts, oci.WithEnv(pairs))
	}

	// specPlatform follows the pull platform so a cross-platform client
	// (macOS worker against a Linux dev containerd) still produces a spec
	// with a Linux section; on a native Linux worker both default to the
	// local platform
	specPlatform := rt.platform
	if specPlatform == "" {
		specPlatform = platforms.DefaultString()
	}

	newContainer := func() (client.Container, error) {
		opts := []client.NewContainerOpts{
			client.WithSnapshotter(rt.snapshotter),
			client.WithNewSnapshot(payload.Name, image),
			withSpecForPlatform(specPlatform, specOpts...),
		}

		if payload.ID != 0 {
			opts = append(opts, client.WithContainerLabels(map[string]string{
				containerIDLabel: strconv.FormatInt(payload.ID, 10),
			}))
		}

		return cd.NewContainer(ctx, payload.Name, opts...)
	}

	container, err := newContainer()
	if err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("create container: %v", err)}
	}

	// the shim writes the task output directly to the file, so it survives
	// the worker process
	task, err := container.NewTask(ctx, cio.LogFile("/tmp/a1s-"+payload.Name+".log"))
	if err != nil {
		container.Delete(ctx, client.WithSnapshotCleanup)
		return commandResult{OK: false, Error: fmt.Sprintf("create task: %v", err)}
	}

	if err := task.Start(ctx); err != nil {
		task.Delete(ctx, client.WithProcessKill)
		container.Delete(ctx, client.WithSnapshotCleanup)
		return commandResult{OK: false, Error: fmt.Sprintf("start task: %v", err)}
	}

	detail, _ := json.Marshal(map[string]any{"pid": task.Pid()})
	return commandResult{OK: true, Detail: detail}
}

// ensureImage returns the image from the local store or pulls it. A locally
// imported image record may lack snapshots (ctr images import stores blobs
// and metadata only), so the Get path unpacks on demand — idempotent for
// already-unpacked images.
func (rt *containerRuntime) ensureImage(ctx context.Context, cd *client.Client, ref string) (client.Image, error) {
	// containerd stores and resolves images under fully-qualified names;
	// short names like "nginx" are normalized docker-style
	named, err := reference.ParseDockerRef(ref)
	if err != nil {
		return nil, fmt.Errorf("invalid image reference %q: %w", ref, err)
	}

	canonical := named.String()

	if image, err := cd.GetImage(ctx, canonical); err == nil {
		if err := image.Unpack(ctx, rt.snapshotter); err != nil {
			return nil, err
		}

		return image, nil
	}

	return rt.pullImage(ctx, cd, canonical)
}

// pullImage fetches the image and unpacks it into the runtime's snapshotter.
func (rt *containerRuntime) pullImage(ctx context.Context, cd *client.Client, ref string) (client.Image, error) {
	pullOpts := []client.RemoteOpt{
		client.WithPullUnpack,
		client.WithPullSnapshotter(rt.snapshotter),
	}
	if rt.platform != "" {
		pullOpts = append(pullOpts, client.WithPlatform(rt.platform))
	}

	return cd.Pull(ctx, ref, pullOpts...)
}

// removeExisting kills and deletes a leftover container from a previous
// attempt, so start is deterministic when re-run.
func (rt *containerRuntime) removeExisting(ctx context.Context, cd *client.Client, name string) error {
	container, err := cd.LoadContainer(ctx, name)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}

		return err
	}

	if task, err := container.Task(ctx, nil); err == nil {
		if err := task.Kill(ctx, syscall.SIGKILL); err == nil {
			waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			exitCh, _ := task.Wait(waitCtx)
			<-exitCh
			cancel()
		}
		task.Delete(ctx, client.WithProcessKill)
	}

	// WithSnapshotCleanup also removes the container's snapshot, which
	// would otherwise outlive the record until the lazy GC and break the
	// deterministic recreation of same-named containers
	return container.Delete(ctx, client.WithSnapshotCleanup)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

// reportStatuses computes the mapped status of every labeled container and
// reports it to the API, so the database reflects reality even without
// commands. Failures are logged and survived.
func (rt *containerRuntime) reportStatuses(ctx context.Context, client *apiClient, workerID int64) {
	cd, err := rt.cdClient(ctx)
	if err != nil {
		log.Printf("report statuses: containerd unavailable: %v", err)
		return
	}

	manifest := make([]manifestEntry, 0, 8)

	containers, err := cd.Containers(ctx)
	if err != nil {
		log.Printf("report statuses: list containers failed: %v", err)
		return
	}

	for _, container := range containers {
		labels, err := container.Labels(ctx)
		if err != nil {
			log.Printf("report statuses: labels for %s failed: %v", container.ID(), err)
			continue
		}

		rawID, ok := labels[containerIDLabel]
		if !ok {
			continue // not an a1s-managed container
		}

		containerID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || containerID <= 0 {
			continue
		}

		status, err := containerStatus(ctx, container)
		if err != nil {
			log.Printf("report statuses: status for %s failed: %v", container.ID(), err)
			continue
		}

		if err := client.reportStatus(ctx, containerID, status); err != nil {
			// a 404 means the DB row is gone: the runtime container is a
			// ghost left behind by a deleted row, so clean it up
			var apiErr *apiError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				log.Printf("report statuses: container %d (%s) has no row, removing ghost", containerID, container.ID())
				rt.removeExisting(ctx, cd, container.ID())
				continue
			}

			log.Printf("report statuses: report for container %d failed: %v", containerID, err)
			continue
		}

		manifest = append(manifest, manifestEntry{ID: containerID, Status: status})
		log.Printf("reported container %d (%s) as %s", containerID, container.ID(), status)
	}

	if err := client.reportManifest(ctx, workerID, manifest); err != nil {
		log.Printf("report manifest failed: %v", err)
	}
}

// containerStatus derives the mapped A1s status of one containerd container
// (same mapping as inspect).
func containerStatus(ctx context.Context, container client.Container) (string, error) {
	task, err := container.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return models.ContainerStopped, nil
		}

		return "", err
	}

	status, err := task.Status(ctx)
	if err != nil {
		return "", err
	}

	return mapTaskStatus(strings.ToUpper(string(status.Status)), status.ExitStatus), nil
}
