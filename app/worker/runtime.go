package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// execute dispatches one command to its action executor. The stop, remove
// and inspect executors land in T3.6; start is live.
func (rt *containerRuntime) execute(ctx context.Context, cmd command) commandResult {
	switch cmd.Action {
	case "start":
		return rt.start(ctx, cmd)
	default:
		return commandResult{OK: false, Error: "action not implemented yet"}
	}
}

type startPayload struct {
	Image   string            `json:"image"`
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

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
		return cd.NewContainer(ctx, payload.Name,
			client.WithSnapshotter(rt.snapshotter),
			client.WithNewSnapshot(payload.Name, image),
			withSpecForPlatform(specPlatform, specOpts...))
	}

	container, err := newContainer()
	if err != nil {
		return commandResult{OK: false, Error: fmt.Sprintf("create container: %v", err)}
	}

	// the shim writes the task output directly to the file, so it survives
	// the worker process
	task, err := container.NewTask(ctx, cio.LogFile("/tmp/a1s-"+payload.Name+".log"))
	if err != nil {
		container.Delete(ctx)
		return commandResult{OK: false, Error: fmt.Sprintf("create task: %v", err)}
	}

	if err := task.Start(ctx); err != nil {
		task.Delete(ctx, client.WithProcessKill)
		container.Delete(ctx)
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
	if image, err := cd.GetImage(ctx, ref); err == nil {
		if err := image.Unpack(ctx, rt.snapshotter); err != nil {
			return nil, err
		}

		return image, nil
	}

	return rt.pullImage(ctx, cd, ref)
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

	return container.Delete(ctx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
