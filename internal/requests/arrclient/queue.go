package arrclient

import "strings"

type QueueResource struct {
	ID                    int    `json:"id,omitempty"`
	MovieID               int    `json:"movieId,omitempty"`
	SeriesID              int    `json:"seriesId,omitempty"`
	Title                 string `json:"title,omitempty"`
	Status                string `json:"status,omitempty"`
	TrackedDownloadStatus string `json:"trackedDownloadStatus,omitempty"`
	TrackedDownloadState  string `json:"trackedDownloadState,omitempty"`
	DownloadID            string `json:"downloadId,omitempty"`
	OutputPath            string `json:"outputPath,omitempty"`
}

type QueueEvaluation struct {
	State          string
	ExternalStatus string
	Message        string
}

const (
	QueueStateQueued      = "queued"
	QueueStateDownloading = "downloading"
	QueueStateFailed      = "failed"
)

func EvaluateQueue(resources []QueueResource) QueueEvaluation {
	if len(resources) == 0 {
		return QueueEvaluation{State: QueueStateQueued, ExternalStatus: "not_in_queue"}
	}

	queued := false
	downloadingStatus := ""
	for _, resource := range resources {
		status := strings.TrimSpace(resource.Status)
		state := strings.TrimSpace(resource.TrackedDownloadState)
		externalStatus := joinStatus(status, state)

		if status == "failed" || state == "failed" || state == "failedPending" {
			return QueueEvaluation{
				State:          QueueStateFailed,
				ExternalStatus: externalStatus,
				Message:        queueFailureMessage(resource, externalStatus),
			}
		}
		if status == "downloading" || state == "downloading" || state == "importPending" || state == "importing" {
			if downloadingStatus == "" {
				downloadingStatus = externalStatus
			}
			queued = true
			continue
		}
		queued = true
	}
	if downloadingStatus != "" {
		return QueueEvaluation{State: QueueStateDownloading, ExternalStatus: downloadingStatus}
	}
	if queued {
		return QueueEvaluation{State: QueueStateQueued, ExternalStatus: "queued"}
	}
	return QueueEvaluation{State: QueueStateQueued, ExternalStatus: "unknown"}
}

func joinStatus(status, state string) string {
	switch {
	case status != "" && state != "":
		return status + "/" + state
	case status != "":
		return status
	case state != "":
		return state
	default:
		return "unknown"
	}
}

func queueFailureMessage(resource QueueResource, externalStatus string) string {
	title := strings.TrimSpace(resource.Title)
	if title == "" {
		return "external queue failed: " + externalStatus
	}
	return "external queue failed for " + title + ": " + externalStatus
}
