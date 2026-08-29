package handlers

import "encoding/base64"

// The neutral placeholder is compiled into the server, outside web/dist and
// the canonical artwork root. The switch keeps every supported image type an
// explicit 200 image response even though the first design shares one asset.
var bundledArtworkPlaceholder = func() []byte {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAQAAAAECAIAAAAmkwkpAAAAFElEQVR4nGNgYGD4z0AEYBxVSFUAAN8AAfGxsM4AAAAASUVORK5CYII=")
	if err != nil {
		panic(err)
	}
	return data
}()

func artworkPlaceholder(imageType string) []byte {
	switch imageType {
	case artworkImagePoster, artworkImageBackdrop, artworkImageLogo, artworkImageStill, artworkImageProfile, artworkImageAvatar, artworkImageLibraryPoster, artworkImageCollectionPoster, artworkImageCollectionBackdrop:
		return bundledArtworkPlaceholder
	default:
		return bundledArtworkPlaceholder
	}
}
