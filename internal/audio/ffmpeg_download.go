package audio

import (
	"archive/tar"
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// Static build download URLs (BtbN/FFmpeg-Builds on GitHub).
var ffmpegDownloadURLs = map[string]string{
	"linux/amd64":   "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linux64-gpl.tar.xz",
	"linux/arm64":   "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linuxarm64-gpl.tar.xz",
	"windows/amd64": "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip",
}

// maxBinarySize is the maximum allowed size for a single extracted binary (500 MB).
const maxBinarySize = 500 * 1024 * 1024

// ensureFFmpeg locates ffmpeg and ffprobe, downloading static builds if necessary.
// Search order: system PATH → {configDir}/bin/ → download to {configDir}/bin/.
func ensureFFmpeg(configDir string) (ffmpegPath, ffprobePath string, err error) {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}

	// 1. Check system PATH
	if fp, lookErr := exec.LookPath("ffmpeg" + ext); lookErr == nil {
		if pp, lookErr := exec.LookPath("ffprobe" + ext); lookErr == nil {
			ffmpegLog.Debug().Str("ffmpeg", fp).Str("ffprobe", pp).Msg("using system ffmpeg")
			return fp, pp, nil
		}
	}

	// 2. Check local bin directory
	binDir := filepath.Join(configDir, "bin")
	localFF := filepath.Join(binDir, "ffmpeg"+ext)
	localFP := filepath.Join(binDir, "ffprobe"+ext)

	if _, statErr := os.Stat(localFF); statErr == nil {
		if _, statErr := os.Stat(localFP); statErr == nil {
			ffmpegLog.Debug().Str("ffmpeg", localFF).Str("ffprobe", localFP).Msg("using local ffmpeg")
			return localFF, localFP, nil
		}
	}

	// 3. Download
	ffmpegLog.Info().Msg("ffmpeg not found, downloading static build...")
	if err := downloadFFmpeg(binDir, ext); err != nil {
		return "", "", err
	}
	return localFF, localFP, nil
}

func downloadFFmpeg(binDir, ext string) error {
	key := runtime.GOOS + "/" + runtime.GOARCH
	dlURL, ok := ffmpegDownloadURLs[key]
	if !ok {
		return fmt.Errorf("no pre-built ffmpeg for %s/%s — install ffmpeg manually", runtime.GOOS, runtime.GOARCH)
	}

	if err := os.MkdirAll(binDir, 0750); err != nil {
		return fmt.Errorf("create bin directory: %w", err)
	}

	ffmpegLog.Info().Str("url", dlURL).Str("platform", key).Msg("downloading ffmpeg")

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(dlURL)
	if err != nil {
		return fmt.Errorf("download ffmpeg: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download ffmpeg: HTTP %d", resp.StatusCode)
	}

	ffmpegLog.Info().Int64("content_length", resp.ContentLength).Msg("download started")

	if strings.HasSuffix(dlURL, ".zip") {
		return extractZip(resp.Body, binDir, ext)
	}
	return extractTarXz(resp.Body, binDir, ext)
}

// extractTarXz streams a tar.xz archive and extracts only ffmpeg and ffprobe.
func extractTarXz(r io.Reader, binDir, ext string) error {
	xzReader, err := xz.NewReader(r)
	if err != nil {
		return fmt.Errorf("create xz reader: %w", err)
	}

	tarReader := tar.NewReader(xzReader)
	extracted := 0

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		outPath, base, ok, err := archiveBinaryOutputPath(binDir, header.Name, ext)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := extractFile(outPath, io.LimitReader(tarReader, maxBinarySize)); err != nil {
			return err
		}

		ffmpegLog.Info().Str("file", base).Msg("extracted")
		extracted++
		if extracted >= 2 {
			break
		}
	}

	if extracted < 2 {
		return fmt.Errorf("archive missing ffmpeg or ffprobe (extracted %d/2)", extracted)
	}
	return nil
}

// extractZip downloads the zip to a temp file (zip needs random access), then extracts binaries.
func extractZip(r io.Reader, binDir, ext string) error {
	tmpFile, err := os.CreateTemp("", "ffmpeg-*.zip")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	written, err := io.Copy(tmpFile, r)
	if err != nil {
		return fmt.Errorf("download to temp: %w", err)
	}
	ffmpegLog.Info().Int64("bytes", written).Msg("download complete, extracting")

	zipReader, err := zip.NewReader(tmpFile, written)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	extracted := 0
	for _, f := range zipReader.File {
		outPath, base, ok, err := archiveBinaryOutputPath(binDir, f.Name, ext)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s in zip: %w", base, err)
		}

		if err := extractFile(outPath, io.LimitReader(rc, maxBinarySize)); err != nil {
			rc.Close()
			return err
		}
		rc.Close()

		ffmpegLog.Info().Str("file", base).Msg("extracted")
		extracted++
		if extracted >= 2 {
			break
		}
	}

	if extracted < 2 {
		return fmt.Errorf("zip missing ffmpeg or ffprobe (extracted %d/2)", extracted)
	}
	return nil
}

func sanitizeArchiveBinaryName(entryName, ext string) (string, bool) {
	normalized := strings.ReplaceAll(strings.TrimSpace(entryName), "\\", "/")
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == "" || strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", false
	}

	base := path.Base(cleaned)
	if base != "ffmpeg"+ext && base != "ffprobe"+ext {
		return "", false
	}
	return base, true
}

func archiveBinaryOutputPath(binDir, entryName, ext string) (string, string, bool, error) {
	base, ok := sanitizeArchiveBinaryName(entryName, ext)
	if !ok {
		return "", "", false, nil
	}

	var target string
	switch base {
	case "ffmpeg" + ext:
		target = "ffmpeg" + ext
	case "ffprobe" + ext:
		target = "ffprobe" + ext
	default:
		return "", "", false, nil
	}

	outPath, err := safeJoinBinPath(binDir, target)
	if err != nil {
		return "", "", false, err
	}
	return outPath, target, true, nil
}

func safeJoinBinPath(binDir, base string) (string, error) {
	root := filepath.Clean(binDir)
	if root == "." || root == "" {
		return "", fmt.Errorf("invalid destination directory")
	}
	outPath := filepath.Clean(filepath.Join(root, base))
	prefix := root + string(os.PathSeparator)
	if outPath != root && !strings.HasPrefix(outPath, prefix) {
		return "", fmt.Errorf("unsafe archive output path")
	}
	return outPath, nil
}

// extractFile writes src to the given path with executable permissions.
func extractFile(path string, src io.Reader) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0750)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	if _, err := io.Copy(out, src); err != nil {
		if closeErr := out.Close(); closeErr != nil {
			ffmpegLog.Debug().Err(closeErr).Str("path", path).Msg("close failed after write error")
		}
		_ = os.Remove(path)
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close %s: %w", filepath.Base(path), err)
	}
	return nil
}
