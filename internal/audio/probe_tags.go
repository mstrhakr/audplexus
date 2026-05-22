package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// ProbeTags returns the format-level metadata tags from an audio file.
// Reads via ffprobe; the freeform iTunes atoms emitted by the
// AudiobookRich profile (asin, series, series-part) come back as plain
// string keys.
func (f *FFmpeg) ProbeTags(ctx context.Context, inputPath string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, f.probePath,
		"-v", "quiet",
		"-show_entries", "format_tags",
		"-of", "json",
		inputPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe tags: %w", err)
	}
	var resp struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("decode ffprobe tags: %w", err)
	}
	if resp.Format.Tags == nil {
		return map[string]string{}, nil
	}
	return resp.Format.Tags, nil
}
