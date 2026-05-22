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

// FileProbe is the rich-probe snapshot used by the Book Details "File
// info" block: format, codec, container/stream technicals, chapter
// count, embedded artwork flag, and the raw tag map. Numeric fields
// stay as their ffprobe-reported strings so the UI can render "—"
// for absent values without a separate "set" flag per field.
type FileProbe struct {
	FormatName      string            `json:"format_name"`
	FormatLongName  string            `json:"format_long_name"`
	DurationSec     float64           `json:"duration_sec"`
	BitRate         string            `json:"bit_rate"`
	Size            int64             `json:"size"`
	NumStreams      int               `json:"num_streams"`
	AudioCodec      string            `json:"audio_codec"`
	AudioCodecLong  string            `json:"audio_codec_long"`
	AudioBitRate    string            `json:"audio_bit_rate"`
	SampleRate      string            `json:"sample_rate"`
	Channels        int               `json:"channels"`
	ChannelLayout   string            `json:"channel_layout"`
	HasArtwork      bool              `json:"has_artwork"`
	ChapterCount    int               `json:"chapter_count"`
	Tags            map[string]string `json:"tags"`
}

// ProbeFile returns a FileProbe with everything the Book Details "File
// info" block surfaces: container/codec technicals, chapter count,
// embedded artwork flag, plus the raw format-level tags. One ffprobe
// invocation that asks for format+streams+chapters in JSON.
func (f *FFmpeg) ProbeFile(ctx context.Context, inputPath string) (*FileProbe, error) {
	cmd := exec.CommandContext(ctx, f.probePath,
		"-v", "quiet",
		"-show_format",
		"-show_streams",
		"-show_chapters",
		"-of", "json",
		inputPath,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe file: %w", err)
	}

	var resp struct {
		Format struct {
			FormatName     string            `json:"format_name"`
			FormatLongName string            `json:"format_long_name"`
			Duration       string            `json:"duration"`
			Size           string            `json:"size"`
			BitRate        string            `json:"bit_rate"`
			NumStreams     int               `json:"nb_streams"`
			Tags           map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			CodecType      string `json:"codec_type"`
			CodecName      string `json:"codec_name"`
			CodecLongName  string `json:"codec_long_name"`
			BitRate        string `json:"bit_rate"`
			SampleRate     string `json:"sample_rate"`
			Channels       int    `json:"channels"`
			ChannelLayout  string `json:"channel_layout"`
			Disposition    map[string]int `json:"disposition"`
		} `json:"streams"`
		Chapters []struct {
			ID int `json:"id"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("decode ffprobe file: %w", err)
	}

	probe := &FileProbe{
		FormatName:     resp.Format.FormatName,
		FormatLongName: resp.Format.FormatLongName,
		BitRate:        resp.Format.BitRate,
		NumStreams:     resp.Format.NumStreams,
		ChapterCount:   len(resp.Chapters),
		Tags:           resp.Format.Tags,
	}
	if probe.Tags == nil {
		probe.Tags = map[string]string{}
	}
	if resp.Format.Duration != "" {
		fmt.Sscanf(resp.Format.Duration, "%f", &probe.DurationSec)
	}
	if resp.Format.Size != "" {
		fmt.Sscanf(resp.Format.Size, "%d", &probe.Size)
	}
	for _, st := range resp.Streams {
		switch st.CodecType {
		case "audio":
			if probe.AudioCodec == "" {
				probe.AudioCodec = st.CodecName
				probe.AudioCodecLong = st.CodecLongName
				probe.AudioBitRate = st.BitRate
				probe.SampleRate = st.SampleRate
				probe.Channels = st.Channels
				probe.ChannelLayout = st.ChannelLayout
			}
		case "video":
			// A video stream on an audiobook means embedded cover
			// art. The attached_pic disposition makes that explicit.
			if st.Disposition["attached_pic"] == 1 || !probe.HasArtwork {
				probe.HasArtwork = true
			}
		}
	}
	return probe, nil
}

