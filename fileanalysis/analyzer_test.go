package fileanalysis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func analyzerFixture(t *testing.T, scan probeData, debug, volume, encoders string) (*Analyzer, *Config) {
	t.Helper()
	directory := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config := &Config{
		FFmpegPath: directory, VideoEncoder: "libx264 -crf 24 -pix_fmt yuv420p",
		VideoScaler: "-maxrate 5500K", AudioEncoder: "aac -b:a 160k",
		VideoBitrateMax: 5_000_000, VolumeAnalysisTime: 240,
	}
	scanJSON, err := json.Marshal(scan)
	if err != nil {
		t.Fatal(err)
	}
	runner := func(_ context.Context, executable string, arguments ...string) (string, int, error) {
		joined := strings.Join(arguments, " ")
		switch {
		case arguments[0] == "-version" && filepath.Base(executable) == "ffmpeg":
			return "ffmpeg version fixture Copyright test\n", 0, nil
		case arguments[0] == "-version":
			return "ffprobe fixture\n", 0, nil
		case strings.Contains(joined, "-print_format json"):
			return string(scanJSON), 0, nil
		case strings.Contains(joined, "-v debug"):
			return debug, 0, nil
		case strings.Contains(joined, "volumedetect"):
			return volume, 0, nil
		case strings.Contains(joined, "-encoders"):
			return encoders, 0, nil
		default:
			if len(arguments) > 0 {
				_ = os.WriteFile(arguments[len(arguments)-1], []byte("optimized"), 0o600)
			}
			return "", 0, nil
		}
	}
	return New(func() Config { return *config }, WithRunner(runner), WithLogger(func(string, ...any) {})), config
}

func validProbeData() probeData {
	return probeData{
		Format: probeFormat{
			FormatName: "mov,mp4,m4a,3gp,3g2,mj2", FormatLongName: "QuickTime / MOV",
			Duration: "1.2", BitRate: "1000000",
		},
		Streams: []probeStream{
			{CodecType: "video", CodecName: "h264", CodecLongName: "H.264", PixelFormat: "yuv420p", Width: 1920, Height: 1080},
			{CodecType: "audio", CodecName: "aac", CodecLongName: "AAC", SampleRate: "48000"},
		},
	}
}

func TestVerifyOrRepairAcceptsCompatibleMediaAndBuildsSpec(t *testing.T) {
	analyzer, _ := analyzerFixture(
		t, validProbeData(), "Before avformat_find_stream_info foo seeks:0 ",
		"mean_volume: -10.0 dB\nmax_volume: -1.0 dB", "",
	)
	path := filepath.Join(t.TempDir(), "valid.mp4")
	if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	gotPath, spec, err := analyzer.VerifyOrRepair(context.Background(), true, false, path, true)
	if err != nil || gotPath != path || spec["duration"] != 2 || spec["width"] != 1920 || spec["height"] != 1080 {
		t.Fatalf("VerifyOrRepair() = %q, %#v, %v", gotPath, spec, err)
	}
	status := analyzer.Status(context.Background(), false, false)
	if status["available"] != true || status["which"] == nil || status["analyze_audio_volume"] != true {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestVerifyOrRepairAggregatesPinnedValidationFailures(t *testing.T) {
	scan := validProbeData()
	scan.Format.FormatName = "avi"
	scan.Format.FormatLongName = "AVI"
	scan.Streams[0].CodecName = "mpeg2video"
	scan.Streams[0].CodecLongName = "MPEG-2"
	scan.Streams[1].CodecName = "pcm_s16le"
	scan.Streams[1].CodecLongName = "PCM"
	analyzer, config := analyzerFixture(
		t, scan, "Before avformat_find_stream_info foo seeks:2 ",
		"mean_volume: -30.0 dB\nmax_volume: -10.0 dB", "",
	)
	config.VideoBitrateMax = 0
	path := filepath.Join(t.TempDir(), "invalid.avi")
	if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := analyzer.VerifyOrRepair(context.Background(), true, false, path, true)
	if err == nil {
		t.Fatal("invalid media was accepted")
	}
	for _, want := range []string{
		"Streamability verification failed:", "Container format", "Video codec",
		"Audio codec", "faststart flag", "Audio is at least five dB lower",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("validation error missing %q: %v", want, err)
		}
	}
}

func TestVerifyOrRepairOptimizesAndReturnsRepairedPath(t *testing.T) {
	scan := validProbeData()
	scan.Streams[0].PixelFormat = "yuv444p"
	analyzer, _ := analyzerFixture(
		t, scan, "Before avformat_find_stream_info foo seeks:0 ",
		"mean_volume: -10.0 dB\nmax_volume: -1.0 dB",
		" V..... libx264 fixture\n A..... aac fixture\n",
	)
	path := filepath.Join(t.TempDir(), "repair.mov")
	if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	gotPath, spec, err := analyzer.VerifyOrRepair(context.Background(), true, true, path, true)
	if err != nil || gotPath != strings.TrimSuffix(path, ".mov")+"_fixed.mp4" || spec["duration"] != 2 {
		t.Fatalf("VerifyOrRepair() = %q, %#v, %v", gotPath, spec, err)
	}
	if data, readErr := os.ReadFile(gotPath); readErr != nil || string(data) != "optimized" {
		t.Fatalf("optimized file = %q, %v", data, readErr)
	}
}

func TestVerifyOrRepairIgnoresNonVideo(t *testing.T) {
	analyzer, _ := analyzerFixture(t, probeData{}, "", "", "")
	path := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(path, []byte("document"), 0o600); err != nil {
		t.Fatal(err)
	}
	gotPath, spec, err := analyzer.VerifyOrRepair(context.Background(), true, true, path, true)
	if err != nil || gotPath != path || len(spec) != 0 {
		t.Fatalf("non-video result = %q, %#v, %v", gotPath, spec, err)
	}
}

func TestSplitCommandPreservesFilterEscapes(t *testing.T) {
	arguments, err := splitCommand(`-vf "scale=if(gte(iw\,ih)\,1920\,-2)" -maxrate 5M`)
	if err != nil || len(arguments) != 4 || arguments[1] != `scale=if(gte(iw\,ih)\,1920\,-2)` {
		t.Fatalf("splitCommand() = %#v, %v", arguments, err)
	}
}
