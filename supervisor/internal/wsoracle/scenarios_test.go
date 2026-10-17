package wsoracle

// Shared in-memory scenarios for the portable tests.

func unattributedCreateCase() WS05Input {
	keep := fileEntry("keep.txt", "K")
	rogue := fileEntry("rogue.txt", "R")
	return WS05Input{
		Baseline:        manifest("baseline", keep),
		Sealed:          manifest("sealed", keep, rogue),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: manifest("post-publication", keep, rogue),
	}
}

func stagedUnattributedCase() WS05Input {
	keep := fileEntry("keep.txt", "K")
	return WS05Input{
		Baseline:        manifest("baseline", keep),
		Sealed:          manifest("sealed", keep, fileEntry("rogue.txt", "R")),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: manifest("post-publication", keep),
	}
}

func noChangeCase() WS05Input {
	keep := fileEntry("keep.txt", "K")
	return WS05Input{
		Baseline:        manifest("baseline", keep),
		Sealed:          manifest("sealed", keep),
		PrePublication:  manifest("pre-publication", keep),
		PostPublication: manifest("post-publication", keep),
	}
}

func staleOverwriteCase() WS06Input {
	return WS06Input{
		Baseline:          manifest("baseline", fileEntry("f.txt", "A")),
		PrePublication:    manifest("pre-publication", fileEntry("f.txt", "H")),
		PostPublication:   manifest("post-publication", fileEntry("f.txt", "P")),
		AffectedPaths:     []string{"f.txt"},
		LockIdentity:      "lock-1",
		PublicationResult: "SUCCEEDED",
	}
}
