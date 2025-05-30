package logs

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/armon/circbuf"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/progress"
	"github.com/pkg/errors"
	"github.com/tonistiigi/units"
)

var defaultMaxLogSize = 2 * 1024 * 1024
var defaultMaxLogSpeed = 200 * 1024 // per second

const (
	stdout = 1
	stderr = 2
)

var configCheckOnce sync.Once

func NewLogStreams(ctx context.Context, printOutput bool) (io.WriteCloser, io.WriteCloser, func()) {
	stdout := newStreamWriter(ctx, stdout, printOutput)
	stderr := newStreamWriter(ctx, stderr, printOutput)
	return stdout, stderr, func() {
		stdout.flushBuffer()
		stderr.flushBuffer()
	}
}

func newStreamWriter(ctx context.Context, stream int, printOutput bool) *streamWriter {
	pw, _, _ := progress.NewFromContext(ctx)
	return &streamWriter{
		pw:          pw,
		stream:      stream,
		printOutput: printOutput,
		created:     time.Now(),
	}
}

type streamWriter struct {
	pw              progress.Writer
	stream          int
	printOutput     bool
	created         time.Time
	size            int
	clipping        bool
	clipReasonSpeed bool
	buf             *circbuf.Buffer
}

func (sw *streamWriter) checkLimit(n int) int {
	configCheckOnce.Do(func() {
		maxLogSize, err := strconv.ParseInt(os.Getenv("BUILDKIT_STEP_LOG_MAX_SIZE"), 10, 32)
		if err == nil {
			defaultMaxLogSize = int(maxLogSize)
		}
		maxLogSpeed, err := strconv.ParseInt(os.Getenv("BUILDKIT_STEP_LOG_MAX_SPEED"), 10, 32)
		if err == nil {
			defaultMaxLogSpeed = int(maxLogSpeed)
		}
	})

	oldSize := sw.size
	sw.size += n

	maxSize := -1
	if defaultMaxLogSpeed != -1 {
		maxSize = int(math.Ceil(time.Since(sw.created).Seconds())) * defaultMaxLogSpeed
		sw.clipReasonSpeed = true
	}
	if maxSize == -1 || maxSize > defaultMaxLogSize {
		maxSize = defaultMaxLogSize
		sw.clipReasonSpeed = false
	}

	if maxSize != -1 {
		if maxSize < oldSize {
			return 0
		}

		if sw.size > maxSize {
			return maxSize - oldSize
		}
	}
	return n
}

func (sw *streamWriter) clipLimitMessage() string {
	if sw.clipReasonSpeed {
		return fmt.Sprintf("%#g/s", units.Bytes(defaultMaxLogSpeed))
	}
	return fmt.Sprintf("%#g", units.Bytes(defaultMaxLogSize))
}

func (sw *streamWriter) Write(dt []byte) (int, error) {
	oldSize := len(dt)
	limit := sw.checkLimit(len(dt))
	if sw.buf == nil && limit < len(dt) {
		var err error
		sw.buf, err = circbuf.NewBuffer(256 * 1024)
		if err != nil {
			return 0, err
		}
	}
	if sw.buf != nil {
		sw.buf.Write(dt)
	}

	dt = slices.Clone(dt[:limit])

	if sw.clipping && oldSize == len(dt) {
		sw.clipping = false
	}
	if !sw.clipping && oldSize != len(dt) {
		dt = append(dt, fmt.Appendf(nil, "\n[output clipped, log limit %s reached]\n", sw.clipLimitMessage())...)
		sw.clipping = true
	}

	_, err := sw.write(dt)
	if err != nil {
		return 0, err
	}
	return oldSize, nil
}

func (sw *streamWriter) write(dt []byte) (int, error) {
	if len(dt) == 0 {
		return 0, nil
	}
	sw.pw.Write(identity.NewID(), client.VertexLog{
		Stream: sw.stream,
		Data:   dt,
	})
	if sw.printOutput {
		switch sw.stream {
		case 1:
			return os.Stdout.Write(dt)
		case 2:
			return os.Stderr.Write(dt)
		default:
			return 0, errors.Errorf("invalid stream %d", sw.stream)
		}
	}
	return len(dt), nil
}

func (sw *streamWriter) flushBuffer() {
	if sw.buf == nil {
		return
	}
	_, _ = sw.write(sw.buf.Bytes())
	sw.buf = nil
}

func (sw *streamWriter) Close() error {
	return sw.pw.Close()
}

func LoggerFromContext(ctx context.Context) func([]byte) {
	return func(dt []byte) {
		pw, _, _ := progress.NewFromContext(ctx)
		defer pw.Close()
		pw.Write(identity.NewID(), client.VertexLog{
			Stream: stderr,
			Data:   dt,
		})
	}
}
