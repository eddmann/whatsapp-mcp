package media

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var ffmpegBin = "ffmpeg"

// SetFFmpegPath allows overriding the ffmpeg binary path via configuration.
func SetFFmpegPath(path string) {
	if path != "" {
		ffmpegBin = path
	}
}

// ConvertToOpusOgg converts an input audio file to .ogg (Opus) using ffmpeg.
// Returns the output path (temporary next to input) without removing the input.
func ConvertToOpusOgg(inputPath string) (string, error) {
	if _, err := os.Stat(inputPath); err != nil {
		return "", fmt.Errorf("input missing: %w", err)
	}
	dir := filepath.Dir(inputPath)
	base := filepath.Base(inputPath)
	out := filepath.Join(dir, base+".converted.ogg")
	cmd := exec.Command(ffmpegBin,
		"-i", inputPath,
		"-c:a", "libopus",
		"-b:a", "32k",
		"-ar", "24000",
		"-application", "voip",
		"-vbr", "on",
		"-compression_level", "10",
		"-frame_duration", "60",
		"-y",
		out,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg failed: %w", err)
	}
	return out, nil
}
