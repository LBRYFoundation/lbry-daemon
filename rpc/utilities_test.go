package rpc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUTXOListAndReleaseRPC(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	listed := fileMutationRPCResult(t, fixture.server, "utxo_list", map[string]any{
		"account_id": fixture.account.ID,
	}).(map[string]any)
	items := listed["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["amount"] != "5.0" ||
		listed["total_items"] != json.Number("1") {
		t.Fatalf("utxo_list = %#v", listed)
	}
	spendables, err := fixture.ledger.Database.ListSpendableOutputs(
		context.Background(), []string{fixture.account.ID},
	)
	if err != nil || len(spendables) != 1 {
		t.Fatalf("spendables = %#v, %v", spendables, err)
	}
	if err := fixture.ledger.Database.ReserveOutputs(
		context.Background(), []string{spendables[0].TXOID}, true,
	); err != nil {
		t.Fatal(err)
	}
	if result := fileMutationRPCResult(t, fixture.server, "utxo_release", map[string]any{
		"account_id": fixture.account.ID,
	}); result != nil {
		t.Fatalf("utxo_release = %#v", result)
	}
	spendables, err = fixture.ledger.Database.ListSpendableOutputs(
		context.Background(), []string{fixture.account.ID},
	)
	if err != nil || len(spendables) != 1 {
		t.Fatalf("released spendables = %#v, %v", spendables, err)
	}
}

func TestFFmpegFindConfiguredProbeAndUnavailableState(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	directory := t.TempDir()
	for name, output := range map[string]string{"ffmpeg": "ffmpeg version test\n", "ffprobe": "ffprobe version test\n"} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '"+output+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.server.settings.Set("ffmpeg_path", directory); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.settings.Set("volume_analysis_time", 0); err != nil {
		t.Fatal(err)
	}
	available := fileMutationRPCResult(t, fixture.server, "ffmpeg_find", map[string]any{}).(map[string]any)
	if available["available"] != true || available["which"] != filepath.Join(directory, "ffmpeg") ||
		available["analyze_audio_volume"] != false {
		t.Fatalf("available ffmpeg = %#v", available)
	}

	if _, err := fixture.server.settings.Set("ffmpeg_path", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", "")
	unavailable := fileMutationRPCResult(t, fixture.server, "ffmpeg_find", map[string]any{}).(map[string]any)
	_ = oldPath
	if unavailable["available"] != false || unavailable["which"] != nil {
		t.Fatalf("unavailable ffmpeg = %#v", unavailable)
	}
}
