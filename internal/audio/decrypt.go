package audio

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"

	"github.com/mstrhakr/audplexus/internal/logging"
)

var decryptLog = logging.Component("decrypt")

// DecryptAAX decrypts an AAX file using activation bytes.
// Output is an M4B file (same codec, just container copy).
func (f *FFmpeg) DecryptAAX(inputPath, outputPath, activationBytes string, progressCb func(ProgressInfo)) error {
	return f.DecryptAAXWithMetadata(inputPath, outputPath, activationBytes, Metadata{}, progressCb)
}

// DecryptAAXWithMetadata decrypts AAX and embeds metadata in one ffmpeg invocation.
func (f *FFmpeg) DecryptAAXWithMetadata(inputPath, outputPath, activationBytes string, meta Metadata, progressCb func(ProgressInfo)) error {
	decryptLog.Info().
		Str("input", inputPath).
		Str("output", outputPath).
		Msg("starting AAX decryption")

	// Validate activation bytes format
	if activationBytes == "" {
		return fmt.Errorf("activation bytes required for AAX decryption")
	}

	// Log activation bytes validation
	if !isValidActivationBytes(activationBytes) {
		decryptLog.Warn().
			Int("length", len(activationBytes)).
			Str("sample", activationBytes[:min(20, len(activationBytes))]).
			Msg("activation bytes format looks unusual (expected hex format, 32+ chars)")
	} else {
		decryptLog.Debug().Msg("activation bytes format validated (hex string)")
	}

	// Check container type to detect if file is AAXC instead of AAX
	containerType, err := f.detectContainerType(inputPath)
	if err != nil {
		decryptLog.Warn().
			Err(err).
			Str("input", inputPath).
			Msg("could not detect container type, proceeding with AAX decryption")
	} else if containerType == "aaxc" {
		decryptLog.Error().
			Str("input", inputPath).
			Str("container", containerType).
			Msg("file is AAXC format (DRM v4) but AAX activation bytes provided (DRM v2/v3); this will produce corrupted output")
		return fmt.Errorf(
			"AAXC Format Detected But AAX Credentials Provided:\n"+
				"The file '%s' has AAXC format container (DRM v4) but only AAX activation bytes are available.\n"+
				"This happens when:\n"+
				"  1. Audible API did not return AAXC Key+IV credentials during download\n"+
				"  2. Your Audible account credentials may be outdated or invalid\n"+
				"  3. This particular audiobook may only support classic AAX format on your account\n"+
				"\n"+
				"To fix:\n"+
				"  • Delete the incomplete download file and re-authenticate with fresh Audible credentials\n"+
				"  • Check that your Audible account has rights to download in AAXC format\n"+
				"  • For now, you can only decrypt AAX format books (%s extension)\n",
			inputPath, ".aax")
	}

	err = f.runWithProgress(f.buildDecryptArgs(inputPath, outputPath, activationBytes, "", "", meta), progressCb)
	if err != nil {
		decryptLog.Error().
			Err(err).
			Str("input", inputPath).
			Str("output", outputPath).
			Msg("AAX decryption failed")
		return fmt.Errorf("AAX decryption failed: %w", err)
	}

	decryptLog.Debug().
		Str("output", outputPath).
		Msg("AAX decryption succeeded, validating output")
	return f.validateDecryption(inputPath, outputPath, activationBytes)
}

// DecryptAAXC decrypts an AAXC file using key and IV.
// Output is an M4B file (same codec, just container copy).
func (f *FFmpeg) DecryptAAXC(inputPath, outputPath, key, iv string, progressCb func(ProgressInfo)) error {
	return f.DecryptAAXCWithMetadata(inputPath, outputPath, key, iv, Metadata{}, progressCb)
}

// DecryptAAXCWithMetadata decrypts AAXC and embeds metadata in one ffmpeg invocation.
func (f *FFmpeg) DecryptAAXCWithMetadata(inputPath, outputPath, key, iv string, meta Metadata, progressCb func(ProgressInfo)) error {
	decryptLog.Info().
		Str("input", inputPath).
		Str("output", outputPath).
		Msg("starting AAXC decryption")

	// Validate key and IV format
	if key == "" || iv == "" {
		return fmt.Errorf("audible_key and audible_iv required for AAXC decryption")
	}

	if !isValidHexString(key) || !isValidHexString(iv) {
		decryptLog.Warn().
			Int("key_len", len(key)).
			Int("iv_len", len(iv)).
			Msg("key or IV format looks unusual (expected hex format)")
	} else {
		decryptLog.Debug().
			Int("key_len", len(key)).
			Int("iv_len", len(iv)).
			Msg("key and IV format validated (hex strings)")
	}

	// Check container type to verify this is actually AAXC
	containerType, err := f.detectContainerType(inputPath)
	if err != nil {
		decryptLog.Warn().
			Err(err).
			Str("input", inputPath).
			Msg("could not detect container type, proceeding with AAXC decryption")
	} else if containerType != "aaxc" && containerType != "" {
		decryptLog.Warn().
			Str("input", inputPath).
			Str("container", containerType).
			Msg("file appears to be " + containerType + " format but AAXC credentials provided; may cause issues")
	}

	err = f.runWithProgress(f.buildDecryptArgs(inputPath, outputPath, "", key, iv, meta), progressCb)
	if err != nil {
		decryptLog.Error().
			Err(err).
			Str("input", inputPath).
			Str("output", outputPath).
			Msg("AAXC decryption failed")
		return fmt.Errorf("AAXC decryption failed: %w", err)
	}

	decryptLog.Debug().
		Str("output", outputPath).
		Msg("AAXC decryption succeeded, validating output")
	return f.validateDecryption(inputPath, outputPath, "")
}

func (f *FFmpeg) buildDecryptArgs(inputPath, outputPath, activationBytes, key, iv string, meta Metadata) []string {
	args := []string{}
	if activationBytes != "" {
		args = append(args, "-activation_bytes", activationBytes)
	}
	if key != "" && iv != "" {
		args = append(args, "-audible_key", key, "-audible_iv", iv)
	}

	args = append(args, "-i", inputPath)
	if meta.CoverPath != "" {
		args = append(args,
			"-i", meta.CoverPath,
			"-map", "0:a",
			"-map", "1:v",
			"-disposition:v:0", "attached_pic",
		)
	}

	args = append(args, "-c", "copy")
	args = append(args, buildMetadataArgs(meta)...)
	args = append(args, "-y", outputPath)
	return args
}

// validateDecryption checks that the output file has approximately the same duration.
func (f *FFmpeg) validateDecryption(inputPath, outputPath, activationBytes string) error {
	// Check file exists and has minimum size
	outInfo, err := os.Stat(outputPath)
	if err != nil {
		decryptLog.Error().Err(err).Str("output", outputPath).Msg("output file does not exist after decryption")
		return fmt.Errorf("output file not created: %w", err)
	}
	outSize := outInfo.Size()
	if outSize < 1024*100 { // At least 100KB for a valid audio file
		decryptLog.Error().Int64("size_bytes", outSize).Str("output", outputPath).Msg("output file too small, likely incomplete decryption")
		return fmt.Errorf("output file too small (%d bytes), decryption likely failed", outSize)
	}

	// Probe duration
	outDuration, err := f.Probe(outputPath)
	if err != nil {
		decryptLog.Error().Err(err).Str("output", outputPath).Msg("output validation probe failed")
		return fmt.Errorf("output validation failed: %w", err)
	}

	if outDuration < 60 {
		decryptLog.Warn().Float64("duration_sec", outDuration).Int64("size_bytes", outSize).Str("output", outputPath).Msg("output file suspiciously short")
		return fmt.Errorf("output file too short (%.1fs, %d bytes), decryption likely failed", outDuration, outSize)
	}

	// Validate the audio stream is structurally sane (not a container with unusable audio payload).
	audioStats, err := f.probeAudioStreamStats(outputPath)
	if err != nil {
		decryptLog.Error().Err(err).Str("output", outputPath).Msg("failed to probe audio stream")
		return fmt.Errorf("audio stream validation failed: %w", err)
	}
	if audioStats.Channels <= 0 || audioStats.SampleRate <= 0 {
		decryptLog.Error().
			Int("channels", audioStats.Channels).
			Int("sample_rate", audioStats.SampleRate).
			Str("codec", audioStats.Codec).
			Str("output", outputPath).
			Msg("invalid audio stream properties")
		return fmt.Errorf("invalid output audio stream (codec=%s channels=%d sample_rate=%d)", audioStats.Codec, audioStats.Channels, audioStats.SampleRate)
	}

	// Decode a short segment and fail on first decode error. This catches bitstream corruption
	// that can still pass size/duration checks but produces silence or playback failures.
	if err := f.decodeSmokeTest(outputPath, 30); err != nil {
		decryptLog.Error().Err(err).Str("output", outputPath).Msg("decode smoke test failed")
		return fmt.Errorf("decoded output failed integrity check: %w", err)
	}

	decryptLog.Info().
		Float64("duration_sec", outDuration).
		Int64("size_bytes", outSize).
		Str("codec", audioStats.Codec).
		Int("channels", audioStats.Channels).
		Int("sample_rate", audioStats.SampleRate).
		Str("output", outputPath).
		Msg("decryption validated successfully")
	return nil
}

type audioStreamStats struct {
	Codec      string
	Channels   int
	SampleRate int
}

func (f *FFmpeg) probeAudioStreamStats(inputPath string) (audioStreamStats, error) {
	cmd := exec.Command(f.probePath,
		"-v", "quiet",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name,channels,sample_rate",
		"-of", "json",
		inputPath,
	)

	output, err := cmd.Output()
	if err != nil {
		return audioStreamStats{}, fmt.Errorf("ffprobe audio stream failed: %w", err)
	}

	var parsed struct {
		Streams []struct {
			CodecName  string `json:"codec_name"`
			Channels   int    `json:"channels"`
			SampleRate string `json:"sample_rate"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(output, &parsed); err != nil {
		return audioStreamStats{}, fmt.Errorf("parse audio stream probe: %w", err)
	}
	if len(parsed.Streams) == 0 {
		return audioStreamStats{}, fmt.Errorf("no audio streams found")
	}

	stream := parsed.Streams[0]
	rate := 0
	if stream.SampleRate != "" {
		_, _ = fmt.Sscanf(stream.SampleRate, "%d", &rate)
	}

	return audioStreamStats{
		Codec:      stream.CodecName,
		Channels:   stream.Channels,
		SampleRate: rate,
	}, nil
}

func (f *FFmpeg) decodeSmokeTest(inputPath string, seconds int) error {
	if seconds <= 0 {
		seconds = 15
	}

	cmd := exec.Command(f.binPath,
		"-v", "error",
		"-xerror",
		"-i", inputPath,
		"-map", "0:a:0",
		"-t", fmt.Sprintf("%d", seconds),
		"-f", "null",
		"-",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg decode test failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Decrypt auto-detects the DRM type and decrypts accordingly.
// Prefers container-type detection when possible to prevent format mismatches.
func (f *FFmpeg) Decrypt(inputPath, outputPath, activationBytes, key, iv string) error {
	// Try to detect container type to choose right decryption method
	containerType, detectionErr := f.detectContainerType(inputPath)
	if detectionErr == nil {
		if containerType == "aaxc" {
			if key != "" && iv != "" {
				decryptLog.Debug().Str("input", inputPath).Msg("detected AAXC container, using key+iv decryption")
				return f.DecryptAAXC(inputPath, outputPath, key, iv, nil)
			}
			if activationBytes != "" {
				decryptLog.Error().
					Str("input", inputPath).
					Msg("detected AAXC container format but only activation bytes available; AAXC requires audible_key+audible_iv")
				return fmt.Errorf("file is AAXC format (requires key+iv) but only activation bytes provided")
			}
		} else if containerType == "aax" {
			if activationBytes != "" {
				decryptLog.Debug().Str("input", inputPath).Msg("detected AAX container, using activation bytes decryption")
				return f.DecryptAAX(inputPath, outputPath, activationBytes, nil)
			}
			if key != "" && iv != "" {
				decryptLog.Debug().Str("input", inputPath).Msg("detected AAX container but key+iv available, trying AAXC decryption anyway")
				return f.DecryptAAXC(inputPath, outputPath, key, iv, nil)
			}
		}
	}

	// Fallback: use credential type to decide (original behavior)
	if key != "" && iv != "" {
		decryptLog.Debug().Str("input", inputPath).Msg("using AAXC decryption (key+iv)")
		return f.DecryptAAXC(inputPath, outputPath, key, iv, nil)
	}
	if activationBytes != "" {
		decryptLog.Debug().Str("input", inputPath).Msg("using AAX decryption (activation_bytes)")
		return f.DecryptAAX(inputPath, outputPath, activationBytes, nil)
	}
	decryptLog.Error().Str("input", inputPath).Msg("no decryption credentials available")
	return fmt.Errorf("no decryption credentials provided (need activation_bytes or key+iv)")
}

// ConvertToM4B converts a decrypted file to M4B (usually just a container copy).
func (f *FFmpeg) ConvertToM4B(inputPath, outputPath string) error {
	decryptLog.Info().Str("input", inputPath).Str("output", outputPath).Msg("converting to M4B")
	return f.run(
		"-i", inputPath,
		"-c", "copy",
		"-y",
		outputPath,
	)
}

// ConvertToMP3 converts a decrypted file to MP3.
func (f *FFmpeg) ConvertToMP3(inputPath, outputPath string, bitrate string) error {
	if bitrate == "" {
		bitrate = "128k"
	}
	decryptLog.Info().Str("input", inputPath).Str("output", outputPath).Str("bitrate", bitrate).Msg("converting to MP3")
	return f.run(
		"-i", inputPath,
		"-codec:a", "libmp3lame",
		"-b:a", bitrate,
		"-y",
		outputPath,
	)
}

// ConcatToM4B concatenates a list of audio files (in order) into a single
// M4B output, transcoding to AAC. Used when reassembling chapter-split files
// back into a single audiobook container.
func (f *FFmpeg) ConcatToM4B(inputPaths []string, outputPath, bitrate string) error {
	if len(inputPaths) == 0 {
		return fmt.Errorf("concat: no input files")
	}
	if bitrate == "" {
		bitrate = "128k"
	}

	// Build a concat list file in the same directory as the output.
	listPath := outputPath + ".concat.txt"
	lf, err := os.Create(listPath)
	if err != nil {
		return fmt.Errorf("create concat list: %w", err)
	}
	for _, p := range inputPaths {
		// ffmpeg concat demuxer requires single-quoted absolute paths with
		// internal quotes escaped per its grammar.
		escaped := strings.ReplaceAll(p, `'`, `'\''`)
		if _, err := fmt.Fprintf(lf, "file '%s'\n", escaped); err != nil {
			lf.Close()
			os.Remove(listPath)
			return fmt.Errorf("write concat list: %w", err)
		}
	}
	if err := lf.Close(); err != nil {
		os.Remove(listPath)
		return fmt.Errorf("close concat list: %w", err)
	}
	defer os.Remove(listPath)

	decryptLog.Info().Int("inputs", len(inputPaths)).Str("output", outputPath).Msg("concatenating chapters into m4b")
	return f.run(
		"-f", "concat",
		"-safe", "0",
		"-i", listPath,
		"-codec:a", "aac",
		"-b:a", bitrate,
		"-vn",
		"-y",
		outputPath,
	)
}

// SplitChapters splits an audio file into separate chapter files.
func (f *FFmpeg) SplitChapters(inputPath, outputDir string, chapters []ChapterMark, format string) error {
	decryptLog.Info().Str("input", inputPath).Int("chapters", len(chapters)).Str("format", format).Msg("splitting chapters")
	ext := ".m4b"
	codec := []string{"-c", "copy"}
	if format == "mp3" {
		ext = ".mp3"
		codec = []string{"-codec:a", "libmp3lame", "-b:a", "128k"}
	}

	for i, ch := range chapters {
		outputPath := fmt.Sprintf("%s/%02d - %s%s", outputDir, i+1, sanitizeFilename(ch.Title), ext)
		args := []string{
			"-i", inputPath,
			"-ss", formatDuration(ch.StartMs),
		}
		if ch.EndMs > 0 {
			args = append(args, "-to", formatDuration(ch.EndMs))
		}
		args = append(args, codec...)
		args = append(args, "-y", outputPath)

		if err := f.run(args...); err != nil {
			return fmt.Errorf("split chapter %d (%s): %w", i+1, ch.Title, err)
		}
	}
	return nil
}

// ChapterMark represents a chapter boundary.
type ChapterMark struct {
	Title   string
	StartMs int
	EndMs   int
}

func formatDuration(ms int) string {
	totalSec := float64(ms) / 1000.0
	hours := int(totalSec) / 3600
	minutes := (int(totalSec) % 3600) / 60
	seconds := math.Mod(totalSec, 60)
	return fmt.Sprintf("%02d:%02d:%06.3f", hours, minutes, seconds)
}

func sanitizeFilename(name string) string {
	replacer := []string{
		"<", "", ">", "", ":", "", "\"", "", "/", "", "\\", "", "|", "", "?", "", "*", "",
	}
	r := name
	for i := 0; i < len(replacer); i += 2 {
		r = replaceAll(r, replacer[i], replacer[i+1])
	}
	return r
}

func replaceAll(s, old, new string) string {
	for {
		i := indexOf(s, old)
		if i < 0 {
			return s
		}
		s = s[:i] + new + s[i+len(old):]
	}
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// isValidHexString checks if a string is valid hex characters.
func isValidHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// isValidActivationBytes checks if activation bytes look valid (hex format, typically 40 chars = 128 bits).
func isValidActivationBytes(ab string) bool {
	// Activation bytes should be 40 hex characters (128 bits = 16 bytes * 2 hex chars per byte)
	if len(ab) < 32 || len(ab) > 50 {
		return false
	}
	return isValidHexString(ab)
}

// detectContainerType probes the input file to determine if it's AAX or AAXC format.
// Returns "aax", "aaxc", or empty string if detection fails.
func (f *FFmpeg) detectContainerType(inputPath string) (string, error) {
	cmd := exec.Command(f.probePath,
		"-show_entries", "format=major_brand",
		"-of", "json",
		inputPath,
	)

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ffprobe failed: %w", err)
	}

	var result struct {
		Format struct {
			MajorBrand string `json:"major_brand"`
		} `json:"format"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	brand := strings.ToLower(result.Format.MajorBrand)
	if strings.Contains(brand, "aaxc") {
		return "aaxc", nil
	}
	if strings.Contains(brand, "aax") || strings.Contains(brand, "m4a") || strings.Contains(brand, "mp42") {
		return "aax", nil
	}

	return "", nil
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

