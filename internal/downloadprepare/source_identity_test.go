package downloadprepare

import "testing"

func TestSourceFingerprintSurvivesTransientInputRenewal(t *testing.T) {
	request := Request{InputPath: "http://127.0.0.1/input/first", SourceIdentity: "media:revision", TargetCodecVideo: "h264", TargetCodecAudio: "aac"}
	first := request.ExecutionFingerprint()
	request.InputPath = "http://127.0.0.1/input/second"
	if request.ExecutionFingerprint() != first {
		t.Fatal("renewable executor handle changed artifact recipe fingerprint")
	}
	request.SourceIdentity = "media:replacement-revision"
	if request.ExecutionFingerprint() == first {
		t.Fatal("changed media representation retained artifact fingerprint")
	}
}
