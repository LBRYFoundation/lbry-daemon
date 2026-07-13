package componentgraph

var legacyDefinitions = []Component{
	{Name: Database},
	{Name: Wallet, DependsOn: []string{Database}},
	{Name: WalletServerPayments, DependsOn: []string{Wallet}},
	{Name: BlobManager, DependsOn: []string{Database}},
	{Name: DHT, DependsOn: []string{UPnP, Database}},
	{Name: HashAnnouncer, DependsOn: []string{DHT, Database}},
	{Name: FileManager, DependsOn: []string{BlobManager, Database, Wallet}},
	{Name: BackgroundDownloader, DependsOn: []string{Database, BlobManager, DiskSpace}},
	{Name: DiskSpace, DependsOn: []string{Database, BlobManager}},
	{Name: Libtorrent},
	{Name: PeerProtocolServer, DependsOn: []string{UPnP, BlobManager, Wallet}},
	{Name: UPnP},
	{Name: ExchangeRateManager},
	{Name: TrackerAnnouncer, DependsOn: []string{FileManager}},
}

var legacyGraph = mustNew(legacyDefinitions)

func LegacyComponents() []Component {
	return legacyGraph.Components()
}

func LegacyStartStages(skipped []string) ([][]string, error) {
	return legacyGraph.StartStages(skipped)
}

func LegacyStopStages(skipped []string) ([][]string, error) {
	return legacyGraph.StopStages(skipped)
}

func mustNew(components []Component) *Graph {
	graph, err := New(components)
	if err != nil {
		panic(err)
	}
	return graph
}
