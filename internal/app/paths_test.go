package app

import (
	"path/filepath"
	"testing"

	"amdl/internal/config"
)

func TestFormatRoot(t *testing.T) {
	cfg := config.ConfigSet{}
	cfg.Paths.Alac = "AM-Lossless"
	cfg.Paths.Atmos = "AM-Atmos"
	cfg.Paths.Aac = "AM-AAC"

	tests := []struct {
		name  string
		atmos bool
		aac   bool
		want  string
	}{
		{name: "alac", want: "AM-Lossless"},
		{name: "atmos", atmos: true, want: "AM-Atmos"},
		{name: "aac", aac: true, want: "AM-AAC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunner(cfg)
			r.Flags.Atmos = tt.atmos
			r.Flags.AAC = tt.aac
			if got := r.formatRoot(); got != tt.want {
				t.Fatalf("formatRoot() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMVSaveDir(t *testing.T) {
	tests := []struct {
		name         string
		artistFolder string
		artist       string
		limitMax     int
		want         string
	}{
		{
			name:         "artist sub folder",
			artistFolder: "{UrlArtistName}",
			artist:       "Rick Astley",
			limitMax:     200,
			want:         filepath.Join("AM-MV", "Rick Astley"),
		},
		{
			name:         "forbidden characters replaced",
			artistFolder: "{ArtistName}",
			artist:       "AC/DC",
			limitMax:     200,
			want:         filepath.Join("AM-MV", "AC_DC"),
		},
		{
			name:         "name truncated to limit",
			artistFolder: "{ArtistName}",
			artist:       "Rick Astley",
			limitMax:     4,
			want:         filepath.Join("AM-MV", "Rick"),
		},
		{
			name:         "artist sub folder disabled",
			artistFolder: "",
			artist:       "Rick Astley",
			limitMax:     200,
			want:         "AM-MV",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.ConfigSet{}
			cfg.Paths.MV = "AM-MV"
			cfg.Paths.ArtistFolder = tt.artistFolder
			cfg.Paths.LimitMax = tt.limitMax
			r := NewRunner(cfg)
			if got := r.mvSaveDir(tt.artist); got != tt.want {
				t.Fatalf("mvSaveDir(%q) = %q, want %q", tt.artist, got, tt.want)
			}
		})
	}
}
