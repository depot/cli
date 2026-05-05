package ci

const logDownloadFilename = "logs.txt"

func canDownloadLogExport(status string) bool {
	return status == "finished"
}
