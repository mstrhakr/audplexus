package audio

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mstrhakr/audplexus/internal/logging"
)

// FFmpeg wraps the ffmpeg and ffprobe binaries for audio processing.
type FFmpeg struct {
	binPath   string
	probePath string
}

// ProgressInfo holds parsed ffmpeg progress state from `-progress pipe:1`.
type ProgressInfo struct {
	Frame      int
	FPS        float64
	Bitrate    string
	TotalSize  int64
	OutTimeMs  int64
	OutTime    string
	DupFrames  int
	DropFrames int
	Speed      string
	Progress   string // e.g. "continue", "end"
}

var ffmpegLog = logging.Component("ffmpeg")

// NewFFmpeg locates or downloads ffmpeg/ffprobe and returns a ready wrapper.
// It checks the system PATH first, then {configDir}/bin/, downloading static
// builds from GitHub as a last resort.
func NewFFmpeg(configDir string) (*FFmpeg, error) {
	ffmpegPath, ffprobePath, err := ensureFFmpeg(configDir)
	if err != nil {
		return nil, err
	}
	ffmpegLog.Info().Str("ffmpeg", ffmpegPath).Str("ffprobe", ffprobePath).Msg("ffmpeg ready")
	return &FFmpeg{binPath: ffmpegPath, probePath: ffprobePath}, nil
}

// run executes an ffmpeg command and returns combined output on error.
func (f *FFmpeg) run(args ...string) error {
	return f.runOptions(false, args...)
}

// runTranscode is run() with -hide_banner / -nostats / -loglevel error
// added so ffmpeg's stderr stays quiet during long re-encodes. Without
// these flags ffmpeg fires hundreds of progress lines per second and
// the parent buffer accumulates into the gigabytes for a 10-hour book.
func (f *FFmpeg) runTranscode(args ...string) error {
	return f.runOptions(true, args...)
}

func (f *FFmpeg) runOptions(quiet bool, args ...string) error {
	finalArgs := args
	if quiet {
		finalArgs = append([]string{"-hide_banner", "-nostats", "-loglevel", "error"}, args...)
	}

	cmd := exec.Command(f.binPath, finalArgs...)

	// Log command with sensitive info redacted
	safeArgs := make([]string, len(args))
	for i, arg := range args {
		if i > 0 && (args[i-1] == "-activation_bytes" || args[i-1] == "-audible_key" || args[i-1] == "-audible_iv") {
			safeArgs[i] = "[REDACTED]"
		} else {
			safeArgs[i] = arg
		}
	}
	fullCmd := append([]string{f.binPath}, safeArgs...)
	ffmpegLog.Debug().Strs("cmd", fullCmd).Msg("executing ffmpeg")

	// Capture only the tail of stderr (up to 64 KB) on error rather than
	// buffering the entire stream. ffmpeg's stderr can grow into the
	// gigabytes during a long re-encode; we only need the last few KB
	// for diagnostics. Stdout is discarded since these invocations
	// don't write meaningful stdout (runWithProgress is the path that
	// captures progress via -progress pipe:1).
	tail := newTailWriter(64 * 1024)
	cmd.Stdout = io.Discard
	cmd.Stderr = tail
	if err := cmd.Run(); err != nil {
		out := strings.TrimSpace(tail.String())
		ffmpegLog.Error().Err(err).Str("output", out).Msg("ffmpeg command failed")
		return fmt.Errorf("ffmpeg failed: %w\noutput: %s", err, out)
	}
	ffmpegLog.Trace().Msg("ffmpeg command succeeded")
	return nil
}

// tailWriter is an io.Writer that retains only the last `size` bytes
// written to it, dropping older bytes from the head. Used to capture a
// bounded slice of ffmpeg stderr for error reporting without holding
// the whole stream in memory.
//
// Not safe for concurrent Write calls. exec.Cmd serializes writes from
// a single child's stderr pipe, so this is fine for our use; do not
// share an instance across goroutines.
type tailWriter struct {
	buf []byte
	max int
}

func newTailWriter(max int) *tailWriter {
	return &tailWriter{max: max}
}

func (t *tailWriter) Write(p []byte) (int, error) {
	if len(p) >= t.max {
		// Single write bigger than the window — keep just the tail.
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return len(p), nil
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		// Slide the window forward by `over` bytes.
		copy(t.buf, t.buf[over:])
		t.buf = t.buf[:t.max]
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return string(t.buf) }

// runWithProgress executes ffmpeg with `-progress pipe:1` and streams parsed progress.
func (f *FFmpeg) runWithProgress(args []string, cb func(ProgressInfo)) error {
	cmdArgs := append([]string{}, args...)
	cmdArgs = append(cmdArgs, "-progress", "pipe:1", "-nostats")

	cmd := exec.Command(f.binPath, cmdArgs...)

	// Log command with sensitive info redacted
	safeArgs := make([]string, len(cmdArgs))
	for i, arg := range cmdArgs {
		if i > 0 && (cmdArgs[i-1] == "-activation_bytes" || cmdArgs[i-1] == "-audible_key" || cmdArgs[i-1] == "-audible_iv") {
			safeArgs[i] = "[REDACTED]"
		} else {
			safeArgs[i] = arg
		}
	}
	fullCmd := append([]string{f.binPath}, safeArgs...)
	ffmpegLog.Debug().Strs("cmd", fullCmd).Msg("executing ffmpeg with progress tracking")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start: %w", err)
	}

	var stderrBuf bytes.Buffer
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stderrBuf, stderr)
		close(stderrDone)
	}()

	var info ProgressInfo
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		val := parts[1]

		switch key {
		case "frame":
			info.Frame = parseInt(val)
		case "fps":
			info.FPS = parseFloat(val)
		case "bitrate":
			info.Bitrate = val
		case "total_size":
			info.TotalSize = parseInt64(val)
		case "out_time_ms":
			info.OutTimeMs = parseInt64(val)
		case "out_time":
			info.OutTime = val
		case "dup_frames":
			info.DupFrames = parseInt(val)
		case "drop_frames":
			info.DropFrames = parseInt(val)
		case "speed":
			info.Speed = val
		case "progress":
			info.Progress = val
			if cb != nil {
				cb(info)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("ffmpeg progress scan: %w", err)
	}

	waitErr := cmd.Wait()
	<-stderrDone
	if waitErr != nil {
		stderrText := strings.TrimSpace(stderrBuf.String())
		// Parse out useful error info from ffmpeg stderr
		ffmpegLog.Error().
			Err(waitErr).
			Str("stderr", stderrText).
			Msg("ffmpeg execution failed")
		if stderrText != "" {
			return fmt.Errorf("ffmpeg failed: %w\noutput: %s", waitErr, stderrText)
		}
		return fmt.Errorf("ffmpeg failed: %w", waitErr)
	}

	ffmpegLog.Info().Msg("ffmpeg completed successfully")
	return nil
}

// Probe returns the duration of an audio file in seconds.
func (f *FFmpeg) Probe(inputPath string) (float64, error) {
	ffmpegLog.Debug().Str("input", inputPath).Msg("probing audio file")

	cmd := exec.Command(f.probePath,
		"-v", "quiet",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		inputPath,
	)

	output, err := cmd.Output()
	if err != nil {
		ffmpegLog.Error().Err(err).Str("input", inputPath).Msg("ffprobe failed")
		return 0, fmt.Errorf("ffprobe failed: %w", err)
	}

	var duration float64
	_, err = fmt.Sscanf(strings.TrimSpace(string(output)), "%f", &duration)
	ffmpegLog.Debug().Float64("duration_sec", duration).Str("input", inputPath).Msg("probe complete")
	return duration, err
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

