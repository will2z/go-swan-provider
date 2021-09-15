package offlineDealAdmin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"swan-provider/common/utils"
	"swan-provider/config"
	"swan-provider/logs"
	"swan-provider/models"
	"time"
)

type DownloadOption struct {
	Out string   `json:"out"`
	Dir string   `json:"dir"`
}

type Aria2Service struct {
	MinerFid string
	OutDir   string
}

type Aria2GetStatusSuccess struct {
	Id 		string             `json:"id"`
	JsonRpc string             `json:"jsonrpc"`
	Result 	*Aria2StatusResult `json:"result"`
}

type Aria2GetStatusFail struct {
	Id 		string             `json:"id"`
	JsonRpc string             `json:"jsonrpc"`
	Error 	*Aria2StatusError  `json:"error"`
}

type Aria2StatusError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Aria2StatusResult struct {
	Bitfield        string                  `json:"bitfield"`
	CompletedLength string                  `json:"completedLength"`
	Connections     string                  `json:"connections"`
	Dir             string                  `json:"dir"`
	DownloadSpeed   string                  `json:"downloadSpeed"`
	ErrorCode       string                  `json:"errorCode"`
	ErrorMessage    string                  `json:"errorMessage"`
	Gid             string                  `json:"gid"`
	NumPieces       string                  `json:"numPieces"`
	PieceLength     string                  `json:"pieceLength"`
	Status          string                  `json:"status"`
	TotalLength     string                  `json:"totalLength"`
	UploadLength    string                  `json:"uploadLength"`
	UploadSpeed     string                  `json:"uploadSpeed"`
	Files           []Aria2StatusResultFile `json:"files"`
}

type Aria2StatusResultFile struct {
	CompletedLength string                     `json:"completedLength"`
	Index           string                     `json:"index"`
	Length          string                     `json:"length"`
	Path            string                     `json:"path"`
	Selected        string                     `json:"selected"`
	Uris            []Aria2StatusResultFileUri `json:"uris"`
}

type Aria2StatusResultFileUri struct {
	Status string `json:"status"`
	Uri    string `json:"uri"`
}

func GetAria2Service() *Aria2Service {
	aria2Service := &Aria2Service{
		MinerFid: config.GetConfig().Main.MinerFid,
		OutDir: config.GetConfig().Aria2.Aria2DownloadDir,
	}

	return aria2Service
}

func  (self *Aria2Service) findNextDealReady2Download(swanClient *utils.SwanClient) *models.OfflineDeal {
	deals := swanClient.GetOfflineDeals(self.MinerFid, DEAL_STATUS_CREATED, "1")
	if len(deals) == 0 {
		deals = swanClient.GetOfflineDeals(self.MinerFid, DEAL_STATUS_WAITING, "1")
	}

	if len(deals)>0{
		offlineDeal := deals[0]
		return &offlineDeal
	}

	return nil
}

func (self *Aria2Service) CheckDownloadStatus4Deal(aria2Client *utils.Aria2Client, swanClient *utils.SwanClient, deal *models.OfflineDeal, gid string) {
	response := aria2Client.GetDownloadStatus(gid)
	aria2GetStatusSuccess := Aria2GetStatusSuccess{}
	err := json.Unmarshal([]byte(response), &aria2GetStatusSuccess)
	if err != nil {
		logs.GetLogger().Error(err)
		return
	}

	if aria2GetStatusSuccess.Result == nil {
		aria2GetStatusFail := Aria2GetStatusFail{}
		err = json.Unmarshal([]byte(response), &aria2GetStatusFail)
		if err != nil {
			logs.GetLogger().Error(err)
		}

		code := aria2GetStatusFail.Error.Code
		message := aria2GetStatusFail.Error.Message
		msg := fmt.Sprintf("Get status for %s, code:%d, message:%s", gid, code, message)
		swanClient.UpdateOfflineDealStatus(deal.Id, DEAL_STATUS_DOWNLOAD_FAILED, msg)
		logs.GetLogger().Error(msg)
		return
	}

	if len(aria2GetStatusSuccess.Result.Files) != 1 {
		note := "Wrong file amount"
		swanClient.UpdateOfflineDealStatus(deal.Id, DEAL_STATUS_DOWNLOAD_FAILED, note)
		logs.GetLogger().Error(note)
		return
	}

	result := aria2GetStatusSuccess.Result
	code := result.ErrorCode
	message := result.ErrorMessage
	status := result.Status
	file := aria2GetStatusSuccess.Result.Files[0]
	filePath := file.Path
	fileSize := utils.GetInt64FromStr(file.Length)
	completedLen := utils.GetInt64FromStr(file.CompletedLength)
	var completePercent int64 = 0
	if fileSize > 0 {
		completePercent = completedLen / fileSize * 100
	}
	downloadSpeed := utils.GetInt64FromStr(result.DownloadSpeed)/1000

	switch status {
	case ARIA2_TASK_STATUS_ERROR:
		note := fmt.Sprintf("Deal status for %s, code:%s, message:%s, status:%s", gid, code, message, status)
		swanClient.UpdateOfflineDealStatus(deal.Id, DEAL_STATUS_DOWNLOAD_FAILED, note)
		logs.GetLogger().Error(note)
	case ARIA2_TASK_STATUS_ACTIVE:
		fileSizeDownloaded := utils.GetFileSize(filePath)
		if deal.Status != DEAL_STATUS_DOWNLOADING {
			swanClient.UpdateOfflineDealStatus(deal.Id, DEAL_STATUS_DOWNLOADING, gid, filePath, utils.GetStrFromInt64(fileSizeDownloaded))
		}
		msg := fmt.Sprintf("Deal downloading, id: %d, file size: %d, complete: %d%%, speed: %dKiB", deal.Id, fileSize, completePercent, downloadSpeed)
		logs.GetLogger().Info(msg)
	case ARIA2_TASK_STATUS_COMPLETE:
		fileSizeDownloaded := utils.GetFileSize(filePath)
		if fileSizeDownloaded >= 0 {
			swanClient.UpdateOfflineDealStatus(deal.Id, DEAL_STATUS_DOWNLOADED, gid, filePath, utils.GetStrFromInt64(fileSizeDownloaded))
		} else {
			note := fmt.Sprintf("File %s not found on", filePath)
			swanClient.UpdateOfflineDealStatus(deal.Id, DEAL_STATUS_DOWNLOAD_FAILED, note, filePath, utils.GetStrFromInt64(fileSize))
			logs.GetLogger().Error(note)
		}
	default:
		note := fmt.Sprintf("Download failed, cause: %s", result.ErrorMessage)
		if note != deal.Note{
			swanClient.UpdateOfflineDealStatus(deal.Id, DEAL_STATUS_DOWNLOAD_FAILED, note, filePath, utils.GetStrFromInt64(fileSize))
		}
		logs.GetLogger().Error(note, " dealId:", strconv.Itoa(deal.Id))
	}
}

func (self *Aria2Service) CheckDownloadStatus(aria2Client *utils.Aria2Client, swanClient *utils.SwanClient) {
	downloadingDeals := swanClient.GetOfflineDeals(self.MinerFid, DEAL_STATUS_DOWNLOADING)

	for _, deal := range downloadingDeals {
		gid := deal.Note
		if len(gid) <= 0 {
			note := "Download gid not found in offline_deals.note"
			if note != deal.Note{
				swanClient.UpdateOfflineDealStatus(deal.Id, DEAL_STATUS_DOWNLOAD_FAILED, note)
			}
			continue
		}

		self.CheckDownloadStatus4Deal(aria2Client, swanClient, &deal, gid)
	}
}

func (self *Aria2Service) StartDownload4Deal(deal *models.OfflineDeal, aria2Client *utils.Aria2Client, swanClient *utils.SwanClient) {
	logs.GetLogger().Info("start downloading deal id ", deal.Id)
	urlInfo, err := url.Parse(deal.SourceFileUrl)
	if err != nil {
		msg := fmt.Sprintf("parse source file url error:%s", err)
		swanClient.UpdateOfflineDealStatus(deal.Id, DEAL_STATUS_DOWNLOAD_FAILED, msg)
		msg = fmt.Sprintf("Deal id:%d, %s", deal.Id, msg)
		logs.GetLogger().Error(msg)
		return
	}

	filename := urlInfo.Path
	if strings.HasPrefix(urlInfo.RawQuery, "filename=") {
		filename = strings.TrimLeft(urlInfo.RawQuery, "filename=")
		filename = utils.GetDir(urlInfo.Path, filename)
	}
	today := time.Now()
	timeStr := fmt.Sprintf("%d%02d", today.Year(), today.Month())
	outDir := utils.GetDir(self.OutDir, strconv.Itoa(deal.UserId), timeStr)
	option := DownloadOption{
		Out: filename,
		Dir: outDir,
	}

	if utils.IsFileExists(outDir, filename) {
		utils.RemoveFile(outDir, filename)
	}

	response := aria2Client.DownloadFile(deal.SourceFileUrl, option)
	logs.GetLogger().Info(response)

	gid := utils.GetFieldStrFromJson(response, "result")
	self.CheckDownloadStatus4Deal(aria2Client, swanClient, deal, gid)
}

func (self *Aria2Service) StartDownload(aria2Client *utils.Aria2Client, swanClient *utils.SwanClient) {
	downloadingDeals := swanClient.GetOfflineDeals(self.MinerFid, DEAL_STATUS_DOWNLOADING)
	countDownloadingDeals := len(downloadingDeals)
	if countDownloadingDeals >= ARIA2_MAX_DOWNLOADING_TASKS {
		return
	}

	for i := 1; i <= ARIA2_MAX_DOWNLOADING_TASKS- countDownloadingDeals; i++ {
		deal2Download := self.findNextDealReady2Download(swanClient)

		if deal2Download == nil {
			break
		}

		self.StartDownload4Deal(deal2Download, aria2Client, swanClient)
		time.Sleep(1 * time.Second)
	}
}

