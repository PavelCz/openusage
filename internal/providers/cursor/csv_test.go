package cursor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCursorCSV_V1(t *testing.T) {
	body := strings.Join([]string{
		`Date,Kind,Model,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens,Cost`,
		`2026-05-01T12:00:00Z,chat,claude-3.5-sonnet,1000,500,200,800,2500,0.0421`,
		`2026-05-01T12:05:00Z,chat,gpt-4o,200,100,0,400,700,Included`,
		`2026-05-01T12:06:00Z,chat,gpt-4o,300,150,50,600,1100,$0.0150`,
	}, "\n")

	records, version, err := parseCursorCSV(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseCursorCSV err: %v", err)
	}
	if version != cursorCSVV1 {
		t.Fatalf("expected v1, got %d", version)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	if records[0].Model != "claude-3.5-sonnet" {
		t.Fatalf("row[0].Model = %q", records[0].Model)
	}
	if records[0].Cost != 0.0421 {
		t.Fatalf("row[0].Cost = %v", records[0].Cost)
	}
	if records[1].Cost != 0 {
		t.Fatalf("row[1].Cost (Included) = %v, want 0", records[1].Cost)
	}
	if records[2].Cost != 0.0150 {
		t.Fatalf("row[2].Cost = %v", records[2].Cost)
	}
	if records[0].InputWithCache != 1000 || records[0].InputWithoutCache != 500 {
		t.Fatalf("row[0] input tokens = %d/%d", records[0].InputWithCache, records[0].InputWithoutCache)
	}
}

func TestParseCursorCSV_V2_WithMaxMode(t *testing.T) {
	body := strings.Join([]string{
		`Date,Kind,Model,Max Mode,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens,Cost`,
		`2026-05-02T09:00:00Z,agent,claude-sonnet-4.5,true,1500,800,300,900,3500,0.0712`,
		`2026-05-02T09:10:00Z,chat,claude-haiku-3.5,false,200,100,0,400,700,0.0021`,
	}, "\n")

	records, version, err := parseCursorCSV(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseCursorCSV err: %v", err)
	}
	if version != cursorCSVV2 {
		t.Fatalf("expected v2, got %d", version)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if !records[0].MaxMode {
		t.Fatal("row[0] MaxMode should be true")
	}
	if records[1].MaxMode {
		t.Fatal("row[1] MaxMode should be false")
	}
}

func TestParseCursorCSV_MalformedHeader(t *testing.T) {
	body := "Foo,Bar,Baz\n1,2,3\n"
	_, _, err := parseCursorCSV(strings.NewReader(body))
	if err == nil {
		t.Fatal("expected error for unrecognized header, got nil")
	}
}

func TestParseCursorCSV_Empty(t *testing.T) {
	records, version, err := parseCursorCSV(strings.NewReader(""))
	if err != nil {
		t.Fatalf("empty file should not error, got: %v", err)
	}
	if len(records) != 0 || version != cursorCSVVersionUnknown {
		t.Fatalf("empty: records=%d version=%d", len(records), version)
	}
}

func TestParseCursorCSVFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.csv")
	body := strings.Join([]string{
		`Date,Kind,Model,Max Mode,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens,Cost`,
		`2026-05-03T08:00:00Z,chat,gpt-4o,false,100,50,0,200,350,0.0099`,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	records, version, err := parseCursorCSVFile(path)
	if err != nil {
		t.Fatalf("parseCursorCSVFile err: %v", err)
	}
	if version != cursorCSVV2 || len(records) != 1 {
		t.Fatalf("v=%d records=%d", version, len(records))
	}
}
