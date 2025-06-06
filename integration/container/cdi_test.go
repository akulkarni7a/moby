package container // import "github.com/docker/docker/integration/container"

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/integration/internal/container"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/docker/testutil"
	"github.com/docker/docker/testutil/daemon"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"
	"gotest.tools/v3/skip"
)

func TestCreateWithCDIDevices(t *testing.T) {
	skip.If(t, testEnv.DaemonInfo.OSType != "linux", "CDI devices are only supported on Linux")
	skip.If(t, testEnv.IsRemoteDaemon, "cannot run cdi tests with a remote daemon")

	ctx := testutil.StartSpan(baseContext, t)

	cwd, err := os.Getwd()
	assert.NilError(t, err)

	d := daemon.New(t)
	d.StartWithBusybox(ctx, t, "--cdi-spec-dir="+filepath.Join(cwd, "testdata", "cdi"))
	defer d.Stop(t)

	apiClient := d.NewClientT(t)

	id := container.Run(ctx, t, apiClient,
		container.WithCmd("/bin/sh", "-c", "env"),
		container.WithCDIDevices("vendor1.com/device=foo"),
	)
	defer apiClient.ContainerRemove(ctx, id, containertypes.RemoveOptions{Force: true})

	inspect, err := apiClient.ContainerInspect(ctx, id)
	assert.NilError(t, err)

	expectedRequests := []containertypes.DeviceRequest{
		{
			Driver:    "cdi",
			DeviceIDs: []string{"vendor1.com/device=foo"},
		},
	}
	assert.Check(t, is.DeepEqual(inspect.HostConfig.DeviceRequests, expectedRequests))

	poll.WaitOn(t, container.IsStopped(ctx, apiClient, id))
	reader, err := apiClient.ContainerLogs(ctx, id, containertypes.LogsOptions{
		ShowStdout: true,
	})
	assert.NilError(t, err)

	actualStdout := new(bytes.Buffer)
	actualStderr := io.Discard
	_, err = stdcopy.StdCopy(actualStdout, actualStderr, reader)
	assert.NilError(t, err)

	outlines := strings.Split(actualStdout.String(), "\n")
	assert.Assert(t, is.Contains(outlines, "FOO=injected"))
}

func TestCDISpecDirsAreInSystemInfo(t *testing.T) {
	skip.If(t, testEnv.DaemonInfo.OSType == "windows") // d.Start fails on Windows with `protocol not available`
	// TODO: This restriction can be relaxed with https://github.com/moby/moby/pull/46158
	skip.If(t, testEnv.IsRootless, "the t.TempDir test creates a folder with incorrect permissions for rootless")

	testCases := []struct {
		description             string
		config                  string
		specDirs                []string
		expectedInfoCDISpecDirs []string
	}{
		{
			description:             "No config returns default CDI spec dirs",
			config:                  `{}`,
			specDirs:                nil,
			expectedInfoCDISpecDirs: []string{"/etc/cdi", "/var/run/cdi"},
		},
		{
			description:             "CDI explicitly enabled with no spec dirs specified returns default",
			config:                  `{"features": {"cdi": true}}`,
			specDirs:                nil,
			expectedInfoCDISpecDirs: []string{"/etc/cdi", "/var/run/cdi"},
		},
		{
			description:             "CDI enabled with specified spec dirs are returned",
			config:                  `{"features": {"cdi": true}}`,
			specDirs:                []string{"/foo/bar", "/baz/qux"},
			expectedInfoCDISpecDirs: []string{"/foo/bar", "/baz/qux"},
		},
		{
			description:             "CDI enabled with empty string as spec dir returns empty slice",
			config:                  `{"features": {"cdi": true}}`,
			specDirs:                []string{""},
			expectedInfoCDISpecDirs: []string{},
		},
		{
			description:             "CDI enabled with empty config option returns empty slice",
			config:                  `{"features": {"cdi": true}, "cdi-spec-dirs": []}`,
			expectedInfoCDISpecDirs: []string{},
		},
		{
			description:             "CDI explicitly disabled with no spec dirs specified returns empty slice",
			config:                  `{"features": {"cdi": false}}`,
			specDirs:                nil,
			expectedInfoCDISpecDirs: []string{},
		},
		{
			description:             "CDI explicitly disabled with specified spec dirs returns empty slice",
			config:                  `{"features": {"cdi": false}}`,
			specDirs:                []string{"/foo/bar", "/baz/qux"},
			expectedInfoCDISpecDirs: []string{},
		},
		{
			description:             "CDI explicitly disabled with empty string as spec dir returns empty slice",
			config:                  `{"features": {"cdi": false}}`,
			specDirs:                []string{""},
			expectedInfoCDISpecDirs: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			var opts []daemon.Option
			d := daemon.New(t, opts...)

			var args []string
			for _, specDir := range tc.specDirs {
				args = append(args, "--cdi-spec-dir="+specDir)
			}
			if tc.config != "" {
				configPath := filepath.Join(t.TempDir(), "daemon.json")

				err := os.WriteFile(configPath, []byte(tc.config), 0o644)
				assert.NilError(t, err)

				args = append(args, "--config-file="+configPath)
			}
			d.Start(t, args...)
			defer d.Stop(t)

			info := d.Info(t)

			assert.Check(t, is.DeepEqual(tc.expectedInfoCDISpecDirs, info.CDISpecDirs))
		})
	}
}
