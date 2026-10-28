package handlers

type filesystemBrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type filesystemBrowseResponse struct {
	Path    string                  `json:"path"`
	Parent  string                  `json:"parent"`
	Entries []filesystemBrowseEntry `json:"entries"`
}

type FilesystemHandler struct{}

func NewFilesystemHandler() *FilesystemHandler { return &FilesystemHandler{} }
