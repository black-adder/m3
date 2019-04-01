	FilePathPrefix string `yaml:"filePathPrefix" validate:"nonzero"`
	WriteBufferSize int `yaml:"writeBufferSize" validate:"min=1"`
	DataReadBufferSize int `yaml:"dataReadBufferSize" validate:"min=1"`
	InfoReadBufferSize int `yaml:"infoReadBufferSize" validate:"min=1"`
	SeekReadBufferSize int `yaml:"seekReadBufferSize" validate:"min=1"`
	ThroughputLimitMbps float64 `yaml:"throughputLimitMbps" validate:"min=0.0"`
	ThroughputCheckEvery int `yaml:"throughputCheckEvery" validate:"nonzero"`
func (p FilesystemConfiguration) ParseNewFileMode() (os.FileMode, error) {
	if p.NewFileMode == nil {
	str := *p.NewFileMode
func (p FilesystemConfiguration) ParseNewDirectoryMode() (os.FileMode, error) {
	if p.NewDirectoryMode == nil {
	str := *p.NewDirectoryMode

// MmapConfiguration returns the effective mmap configuration.
func (p FilesystemConfiguration) MmapConfiguration() MmapConfiguration {
	if p.Mmap == nil {
		return DefaultMmapConfiguration()
	}
	return *p.Mmap
}