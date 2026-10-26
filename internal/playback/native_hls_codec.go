package playback

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

// nativeHLSVideoCodec derives the master attribute from the emitted init,
// including the actual HEVC constraint flags and converted Dolby Vision profile.
func nativeHLSVideoCodec(data []byte) string {
	if len(data) == 0 || len(data) > 2<<20 {
		return ""
	}
	file, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil || file.Init == nil || file.Init.Moov == nil || len(file.Init.Moov.Traks) != 1 {
		return ""
	}
	track := file.Init.Moov.Trak
	if track == nil || track.Mdia == nil || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil {
		return ""
	}
	stsd := track.Mdia.Minf.Stbl.Stsd
	if entry := stsd.HvcX; entry != nil && entry.HvcC != nil {
		if config := entry.DoViConfig; config != nil && config.RPUPresentFlag && config.BLPresentFlag && config.DVLevel > 0 {
			return fmt.Sprintf("%s.%02d.%02d", entry.Type(), config.DVProfile, config.DVLevel)
		}
		config := entry.HvcC
		return hevc.CodecString(entry.Type(), &hevc.SPS{ProfileTierLevel: hevc.ProfileTierLevel{GeneralProfileSpace: config.GeneralProfileSpace, GeneralTierFlag: config.GeneralTierFlag, GeneralProfileIDC: config.GeneralProfileIDC, GeneralProfileCompatibilityFlags: config.GeneralProfileCompatibilityFlags, GeneralConstraintIndicatorFlags: config.GeneralConstraintIndicatorFlags, GeneralLevelIDC: config.GeneralLevelIDC}})
	}
	if entry := stsd.AvcX; entry != nil && entry.AvcC != nil {
		config := entry.AvcC
		return fmt.Sprintf("%s.%02X%02X%02X", entry.Type(), config.AVCProfileIndication, config.ProfileCompatibility, config.AVCLevelIndication)
	}
	return ""
}
