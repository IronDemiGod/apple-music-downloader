package config

// Config represents the root application configuration grouped into 5 domains.
type Config struct {
	General  GeneralConfig  `koanf:"general"`
	Media    MediaConfig    `koanf:"media"`
	Paths    PathsConfig    `koanf:"paths"`
	Metadata MetadataConfig `koanf:"metadata"`
	Convert  ConvertConfig  `koanf:"convert"`
}

// ConfigSet is an alias for Config to maintain backward compatibility.
type ConfigSet = Config

// GeneralConfig holds account, network, and runtime system settings.
type GeneralConfig struct {
	Storefront         string `koanf:"storefront"`
	MediaUserToken     string `koanf:"media-user-token"`
	AuthorizationToken string `koanf:"authorization-token"`
	Language           string `koanf:"language"`
	Proxy              string `koanf:"proxy"`
	LiteServer         string `koanf:"lite-server"`
	MaxMemoryLimit     int    `koanf:"max-memory-limit"`
	ExitOnError        bool   `koanf:"exit-on-error"`
	ParallelDownloads  int    `koanf:"parallel-downloads"`
}

// MediaConfig holds audio quality, m3u8 detection mode, and video settings.
type MediaConfig struct {
	GetM3u8Mode string   `koanf:"get-m3u8-mode"`
	AlacMax     int      `koanf:"alac-max"`
	AtmosMax    int      `koanf:"atmos-max"`
	AacType     string   `koanf:"aac-type"`
	ALACFix     bool     `koanf:"alac-fix"`
	MV          MVConfig `koanf:"mv"`
}

// MVConfig holds music video audio format and quality constraints.
type MVConfig struct {
	AudioType string `koanf:"audio-type"`
	Max       int    `koanf:"max"`
}

// PathsConfig holds destination folder paths and naming templates for downloaded content.
type PathsConfig struct {
	Alac           string `koanf:"alac"`
	Atmos          string `koanf:"atmos"`
	Aac            string `koanf:"aac"`
	MV             string `koanf:"mv"`
	AlbumFolder    string `koanf:"album-folder"`
	PlaylistFolder string `koanf:"playlist-folder"`
	// PlaylistAlbumFolders saves playlist tracks in their own Artist/Album
	// folders, the way a single-song download does, instead of collecting the
	// whole playlist in one folder named after it.
	PlaylistAlbumFolders bool   `koanf:"playlist-album-folders"`
	// WorkDir, when set, is where tracks are downloaded and processed (defrag,
	// tags, conversion); only the finished file is moved to the save folder.
	// A RAM disk such as /dev/shm/amdl avoids slow save folders entirely.
	WorkDir string `koanf:"work-dir"`
	ArtistFolder         string `koanf:"artist-folder"`
	SongFile             string `koanf:"song-file"`
	LimitMax             int    `koanf:"limit-max"`
	Explicit             string `koanf:"explicit"`
	Clean                string `koanf:"clean"`
	AppleMaster          string `koanf:"apple-master"`
}

// MetadataConfig holds lyrics, artwork, and tags configurations.
type MetadataConfig struct {
	Lyrics  LyricsConfig  `koanf:"lyrics"`
	Artwork ArtworkConfig `koanf:"artwork"`
	Tags    TagsConfig    `koanf:"tags"`
}

// LyricsConfig holds lyrics fetching, formatting, and embedding options.
type LyricsConfig struct {
	SaveFile bool   `koanf:"save-file"`
	Embed    bool   `koanf:"embed"`
	Type     string `koanf:"type"`
	Format   string `koanf:"format"`
	Extra    string `koanf:"extra"`
}

// ArtworkConfig holds cover art and animated artwork options.
type ArtworkConfig struct {
	Embed         bool   `koanf:"embed"`
	Size          string `koanf:"size"`
	Format        string `koanf:"format"`
	SaveArtist    bool   `koanf:"save-artist"`
	SaveAnimated  bool   `koanf:"save-animated"`
	EmbyAnimated  bool   `koanf:"emby-animated"`
	DlForPlaylist bool   `koanf:"dl-for-playlist"`
}

// TagsConfig holds audio metadata tag formatting and identifiers.
type TagsConfig struct {
	SortOrder              bool `koanf:"sort-order"`
	ItunesID               bool `koanf:"itunes-id"`
	UseSongInfoForPlaylist bool `koanf:"use-songinfo-for-playlist"`
}

// ConvertConfig holds post-download audio conversion settings via FFmpeg.
type ConvertConfig struct {
	AfterDownload       bool   `koanf:"after-download"`
	Format              string `koanf:"format"`
	KeepOriginal        bool   `koanf:"keep-original"`
	SkipIfSourceMatch   bool   `koanf:"skip-if-source-matches"`
	FFmpegPath          string `koanf:"ffmpeg-path"`
	ExtraArgs           string `koanf:"extra-args"`
	WithMetadata        bool   `koanf:"with-metadata"`
	WarnLossyToLossless bool   `koanf:"warn-lossy-to-lossless"`
	SkipLossyToLossless bool   `koanf:"skip-lossy-to-lossless"`
	CheckBadALAC        bool   `koanf:"check-bad-alac"`
	DeleteBadALAC       bool   `koanf:"delete-bad-alac"`
}
