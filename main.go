package main

import (
	"encoding/csv"
	"encoding/gob"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/DeweiFeng/6.5610-project/search/database"
	"github.com/DeweiFeng/6.5610-project/search/protocol"
	"github.com/DeweiFeng/6.5610-project/search/utils"
)

func argumentsValidation(preamble string, topk int, query string) {
	if preamble == "" {
		panic("Error: Preamble is required")
	}
	if topk <= 0 {
		panic("Error: topk must be a positive integer")
	}
	// query is empty or a csv file
	if query != "" && filepath.Ext(query) != ".csv" {
		panic("Error: when specified, query must be a csv file")
	}
	// query must be inside the same directory as preamble
	if query != "" {
		dir := filepath.Dir(preamble)
		if filepath.Dir(query) != dir {
			panic("Error: query must be in the same directory as indicated by preamble")
		}
	}
}

func readQueryLine(reader *csv.Reader, dim uint64, precBits uint64) (uint64, []int8, bool) {
	row, err := reader.Read()
	if err == io.EOF {
		return 0, nil, true
	}
	if err != nil {
		panic("Error reading query line: " + err.Error())
	}
	if len(row) != int(dim)+1 {
		panic(fmt.Sprintf("Error: expected %d columns, got %d", dim+1, len(row)))
	}
	clusterIndex, err := utils.StringToUint64(row[0])
	if err != nil {
		panic("Error converting cluster index to uint64: " + err.Error())
	}
	query := make([]int8, dim)
	for i := 0; i < int(dim); i++ {
		u, err := strconv.ParseFloat(row[i+1], 64)
		query[i] = utils.QuantizeClamp(u, precBits)
		if err != nil {
			panic("Error converting query to int8: " + err.Error())
		}
	}
	return clusterIndex, query, false
}

type QueryPerf struct {
	clientHintQueryTime       time.Duration
	serverHintAnswerTime      time.Duration
	clientHintApplyTime       time.Duration
	clientQueryProcessingTime time.Duration
	serverComputeTime         time.Duration
	clientReconTime           time.Duration
	hintQuerySize             uint64
	hintAnsSize               uint64
	querySize                 uint64
	ansSize                   uint64
}

func writeResults(writer *csv.Writer, perfWriter *csv.Writer, scores *[]protocol.VectorScore, k int, perf *QueryPerf) {
	if len(*scores) == 0 {
		panic("Error: No scores to write")
	}
	numRes := k
	if numRes > len(*scores) {
		numRes = len(*scores)
	}
	line := make([]string, numRes*2)
	for i := 0; i < numRes; i++ {
		line[i*2] = fmt.Sprintf("%d", (*scores)[i].ClusterID)
		line[i*2+1] = fmt.Sprintf("%d", (*scores)[i].IDWithinCluster)
	}
	if err := writer.Write(line); err != nil {
		panic("Error writing to output file: " + err.Error())
	}
	writer.Flush()

	perfLine := []string{
		fmt.Sprintf("%g", perf.clientHintQueryTime.Seconds()),
		fmt.Sprintf("%g", perf.serverHintAnswerTime.Seconds()),
		fmt.Sprintf("%g", perf.clientHintApplyTime.Seconds()),
		fmt.Sprintf("%g", perf.clientQueryProcessingTime.Seconds()),
		fmt.Sprintf("%g", perf.serverComputeTime.Seconds()),
		fmt.Sprintf("%g", perf.clientReconTime.Seconds()),
		fmt.Sprintf("%d", perf.hintQuerySize),
		fmt.Sprintf("%d", perf.hintAnsSize),
		fmt.Sprintf("%d", perf.querySize),
		fmt.Sprintf("%d", perf.ansSize),
	}
	if err := perfWriter.Write(perfLine); err != nil {
		panic("Error writing to performance output file: " + err.Error())
	}
	perfWriter.Flush()
}

func filesValidation(preamble string, query string) {
	// we check if preamble_metadata.json is present
	metadataFile := preamble + "_metadata.json"
	if _, err := os.Stat(metadataFile); os.IsNotExist(err) {
		panic("Error: metadata file does not exist: " + metadataFile)
	}
	var queryFile string
	if query != "" {
		// check if preamble_query.csv is present
		queryFile = query
	} else {
		queryFile = preamble + "_query.csv"
	}
	if _, err := os.Stat(queryFile); os.IsNotExist(err) {
		panic("Error: query file does not exist: " + queryFile)
	}
	// check if prefix_cluster_0.csv is present
	clusterFile := preamble + "_cluster_0.csv"
	if _, err := os.Stat(clusterFile); os.IsNotExist(err) {
		panic("Error: cluster file does not exist: " + clusterFile)
	}
}

func logHintSize(hint *protocol.TiptoeHint) uint64 {
	gob.Register(database.Metadata{})
	total := utils.MessageSizeBytes(hint.Metadata)

	gob.Register(database.ClusterMap{})
	h := utils.MessageSizeBytes(hint.PIRHint)
	m := utils.MessageSizeBytes(hint.IndexMap)
	total += (h + m)

	return total
}

func main() {
	preamble := flag.String("preamble", "", "Preamble to use for the search")
	query := flag.String("query", "", "Path to the query file to use for the search")
	topK := flag.Int("topk", 10, "Number of top results to return")
	precBits := flag.Uint64("precBits", 5, "Number of bits to use for precision")
	clusterOnly := flag.Bool("clusterOnly", false, "Only return top k among vectors in the specified cluster")

	flag.Parse()
	argumentsValidation(*preamble, *topK, *query)

	filesValidation(*preamble, *query)

	fmt.Printf("Preamble: %s\n", *preamble)
	fmt.Printf("Query location: %s\n", *query)
	fmt.Printf("Top K: %d\n", *topK)
	fmt.Printf("Cluster Only: %t\n", *clusterOnly)

	dir := filepath.Dir(*preamble)
	prefix := filepath.Base(*preamble)

	var queryFile *os.File
	if *query != "" {
		queryFile = utils.OpenFile(*query)
	} else {
		queryFile = utils.OpenFile(filepath.Join(dir, prefix+"_query.csv"))
	}
	defer queryFile.Close()

	reader := csv.NewReader(queryFile)

	outputFileSuffix := "_results.csv"
	if *clusterOnly {
		outputFileSuffix = "_results_cluster_only.csv"
	}
	var outputFileName string
	if *query != "" {
		outputFileName = (*query)[:len(*query)-4] + outputFileSuffix
	} else {
		outputFileName = filepath.Join(dir, prefix+outputFileSuffix)
	}
	outputFile, err := os.Create(outputFileName)
	if err != nil {
		panic("Error creating output file: " + err.Error())
	}
	defer outputFile.Close()
	writer := csv.NewWriter(outputFile)
	defer writer.Flush()

	fmt.Printf("%s writing vector search results to %s\n", time.Now().Format("2006/01/02 15:04:05"), outputFileName)

	perfFileSuffix := "_perf.csv"
	if *clusterOnly {
		perfFileSuffix = "_perf_cluster_only.csv"
	}
	var perfFileName string
	if *query != "" {
		perfFileName = (*query)[:len(*query)-4] + perfFileSuffix
	} else {
		perfFileName = filepath.Join(dir, prefix+perfFileSuffix)
	}
	perfFile, err := os.Create(perfFileName)
	if err != nil {
		panic("Error creating performance output file: " + err.Error())
	}
	defer perfFile.Close()
	perfWriter := csv.NewWriter(perfFile)
	defer perfWriter.Flush()

	fmt.Printf("%s writing performance statistics to %s\n", time.Now().Format("2006/01/02 15:04:05"), perfFileName)

	// write the header for the perf csv
	perfHeader := []string{
		"clientHintQueryTime",
		"serverHintAnswerTime",
		"clientHintApplyTime",
		"clientQueryProcessingTime",
		"serverComputeTime",
		"clientReconTime",
		"hintQuerySize",
		"hintAnsSize",
		"querySize",
		"ansSize",
	}
	if err := perfWriter.Write(perfHeader); err != nil {
		panic("Error writing to performance output file: " + err.Error())
	}
	perfWriter.Flush()

	// start a timer
	serverPreProcessingStart := time.Now()
	metadata, clusters := database.ReadAllClusters(*preamble, *precBits)
	hintSz := uint64(900)

	server := new(protocol.Server)
	server.ProcessVectorsFromClusters(metadata, clusters, hintSz, *precBits)

	serverPreProcessingTime := time.Since(serverPreProcessingStart)

	fmt.Printf("%s Server database construction time: %s\n", time.Now().Format("2006/01/02 15:04:05"), serverPreProcessingTime)

	// print server hint size in bytes
	fmt.Printf("Server hint size: %d bytes\n", logHintSize(server.Hint))

	client := new(protocol.Client)
	client.Setup(server.Hint)

	queryCount := 0
	for {
		clusterIndex, query, isEnd := readQueryLine(reader, metadata.Dim, *precBits)
		if isEnd {
			break
		}
		sortedScores, perf := runRound(client, server, query, clusterIndex, *clusterOnly)
		writeResults(writer, perfWriter, sortedScores, *topK, perf)
		queryCount++

		if queryCount%100 == 0 {
			fmt.Printf("%s Processed %d queries\n", time.Now().Format("2006/01/02 15:04:05"), queryCount)
		}
	}
}

func runRound(c *protocol.Client, s *protocol.Server, query []int8, clusterIndex uint64, clusterOnly bool) (*[]protocol.VectorScore, *QueryPerf) {
	clientHintQuery := time.Now()
	ct := c.PreprocessQuery()
	clientHintQueryTime := time.Since(clientHintQuery)
	hintQuerySize := utils.MessageSizeBytes(*ct)

	serverHintAnswerStart := time.Now()
	offlineAns := s.HintAnswer(ct)
	serverHintAnswerTime := time.Since(serverHintAnswerStart)
	hintAnsSize := utils.MessageSizeBytes(*offlineAns)

	clientHintApplyStart := time.Now()
	c.ProcessHintApply(offlineAns)
	clientHintApplyTime := time.Since(clientHintApplyStart)

	clientQueryProcessingStart := time.Now()
	queryEmb := c.QueryEmbeddings(query, clusterIndex)
	clientQueryProcessingTime := time.Since(clientQueryProcessingStart)

	querySize := utils.MessageSizeBytes(*queryEmb)

	serverComputeStart := time.Now()
	ans := s.Answer(queryEmb)
	serverComputeTime := time.Since(serverComputeStart)
	ansSize := utils.MessageSizeBytes(*ans)

	var recon *[]protocol.VectorScore

	clientReconStart := time.Now()
	if clusterOnly {
		recon = c.ReconstructWithinCluster(ans, clusterIndex, c.DBInfo.P())
	} else {
		recon = c.ReconstructWithinBin(ans, clusterIndex, c.DBInfo.P())
	}
	clientReconTime := time.Since(clientReconStart)

	perf := &QueryPerf{
		clientHintQueryTime:       clientHintQueryTime,
		serverHintAnswerTime:      serverHintAnswerTime,
		clientHintApplyTime:       clientHintApplyTime,
		clientQueryProcessingTime: clientQueryProcessingTime,
		serverComputeTime:         serverComputeTime,
		clientReconTime:           clientReconTime,
		hintQuerySize:             hintQuerySize,
		hintAnsSize:               hintAnsSize,
		querySize:                 querySize,
		ansSize:                   ansSize,
	}

	return recon, perf
}
