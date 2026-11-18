package apiv2

type fakeWatchTogetherCapability struct{ available bool }

func (f *fakeWatchTogetherCapability) WatchTogetherAvailable() bool { return f.available }

func wireWatchTogetherCapabilityFakes(deps *Dependencies) {
	deps.WatchTogetherStage = new(fakeRoomStage)
	deps.WatchTogetherStart = new(fakeRoomStart)
	deps.WatchTogetherStop = new(fakeRoomStop)
	deps.WatchTogetherSelectionMode = new(fakeRoomSelectionMode)
	deps.WatchTogetherMemberState = new(fakeMemberState)
	deps.WatchTogetherPicker = new(fakePicker)
	deps.WatchTogetherSuggestionPromote = new(fakePromotion)
	deps.WatchTogetherSocket = new(fakeRoomSocket)
	deps.CatalogAccess = new(fakeCatalog)
}

func watchTogetherCapabilityFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "watch_together_capabilities", operationID: "getWatchTogetherCapabilities", method: "GET", path: Prefix + "/watch-together/capabilities", headers: bearer(memberToken), status: 200, schema: "#/components/schemas/WatchTogetherCapabilities", assertHeaders: []string{"Content-Type", "Cache-Control"}, scenario: "An authenticated account reads which watch-together room behaviors the server supports."}}
}
