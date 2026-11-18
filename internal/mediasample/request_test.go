package mediasample

func validRequest() Request {
	return Request{
		Input:  "/media/a.mkv",
		Window: &Window{StartSeconds: 10, DurationSeconds: 20},
		Audio:  &AudioOutput{Fingerprint: true},
	}
}

func validStats() *StatsOutput {
	return &StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 480, BlackThresholds: []int{20, 26, 32}}
}
