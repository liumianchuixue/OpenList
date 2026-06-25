package guangyapan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

const (
	accountBaseURL = "https://account.guangyapan.com"
	apiBaseURL     = "https://api.guangyapan.com"
	defaultClient  = "aMe-8VSlkrbQXpUR"
)

type GuangYaPan struct {
	model.Storage
	Addition

	accountClient *resty.Client
	apiClient     *resty.Client

	resolvedRootFolderID string
	rootFolderResolved   bool
}

func (d *GuangYaPan) Config() driver.Config {
	return config
}

func (d *GuangYaPan) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *GuangYaPan) Init(ctx context.Context) error {
	d.ClientID = strings.TrimSpace(d.ClientID)
	if d.ClientID == "" {
		d.ClientID = defaultClient
	}
	d.DeviceID = normalizeDeviceID(d.DeviceID)
	if d.DeviceID == "" {
		d.DeviceID = randomDeviceID()
	}
	if d.PageSize <= 0 {
		d.PageSize = 100
	}
	if d.OrderBy < 0 {
		d.OrderBy = 3
	}
	if d.SortType != 0 && d.SortType != 1 {
		d.SortType = 1
	}
	d.RootPath = strings.TrimSpace(d.RootPath)
	d.AccessToken = strings.TrimSpace(d.AccessToken)
	d.RefreshToken = strings.TrimSpace(d.RefreshToken)
	d.PhoneNumber = strings.TrimSpace(d.PhoneNumber)
	d.VerifyCode = strings.TrimSpace(d.VerifyCode)
	d.CaptchaToken = strings.TrimSpace(d.CaptchaToken)
	d.VerificationID = strings.TrimSpace(d.VerificationID)

	d.resolvedRootFolderID = ""
	d.rootFolderResolved = false

	d.accountClient = base.NewRestyClient().
		SetBaseURL(accountBaseURL).
		SetHeader("Accept", "application/json, text/plain, */*").
		SetHeader("Content-Type", "application/json").
		SetHeader("X-Device-Model", "chrome%2F147.0.0.0").
		SetHeader("X-Device-Name", "PC-Chrome").
		SetHeader("X-Device-Sign", "wdi10."+d.DeviceID)

	d.apiClient = base.NewRestyClient().
		SetBaseURL(apiBaseURL).
		SetHeader("Accept", "application/json, text/plain, */*").
		SetHeader("Content-Type", "application/json").
		SetHeader("X-Device-Model", "chrome%2F147.0.0.0").
		SetHeader("X-Device-Name", "PC-Chrome").
		SetHeader("X-Device-Sign", "wdi10."+d.DeviceID)

	if d.SendCode {
		if err := d.prepareSMSCode(ctx); err != nil {
			log.Errorf("prepare sms code failed: %s", err)
			d.setTempStatus(err.Error())
			return nil
		}
	}

	if err := d.ensureAccessToken(ctx); err != nil {
		log.Errorf("ensure access token failed: %s", err)
		d.setTempStatus(err.Error())
		return nil
	}

	d.setTempStatus("WORK")
	return nil
}

func (d *GuangYaPan) Drop(ctx context.Context) error {
	return nil
}

func (d *GuangYaPan) GetRootPath(ctx context.Context) (model.RootPath, error) {
	if d.RootPath == "" || d.RootPath == "/" {
		return model.NewRootPath("/", ""), nil
	}
	if d.rootFolderResolved {
		return model.NewRootPath(d.RootPath, d.resolvedRootFolderID), nil
	}

	var out listResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/list_file", map[string]any{
		"parentId": "0",
		"pageNum":  1,
		"pageSize": 1,
		"orderBy":  d.OrderBy,
		"sortType": d.SortType,
		"fileName": d.RootPath,
	}, &out); err != nil {
		return model.NewRootPath(d.RootPath, ""), err
	}

	if out.Code != 0 {
		return model.NewRootPath(d.RootPath, ""), fmt.Errorf("list root path failed: code=%d msg=%s", out.Code, out.Msg)
	}

	for _, item := range out.Data.List {
		if item.FileName == d.RootPath {
			d.resolvedRootFolderID = item.FileID
			d.rootFolderResolved = true
			return model.NewRootPath(d.RootPath, item.FileID), nil
		}
	}
	return model.NewRootPath(d.RootPath, ""), fmt.Errorf("root path not found: %s", d.RootPath)
}

func (d *GuangYaPan) GetStorage(ctx context.Context) *model.Storage {
	return &d.Storage
}

func (d *GuangYaPan) SetStorage(storage model.Storage) error {
	d.Storage = storage
	return nil
}

func (d *GuangYaPan) List(ctx context.Context, dir model.PathInfo, args model.ListArgs) ([]model.Object, error) {
	folderID := dir.GetID()
	if folderID == "" {
		folderID = "0"
	}

	allFiles := make([]fileItem, 0)
	pageNum := 1

	for {
		var out listResp
		if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/list_file", map[string]any{
			"parentId": folderID,
			"pageNum":  pageNum,
			"pageSize": d.PageSize,
			"orderBy":  d.OrderBy,
			"sortType": d.SortType,
		}, &out); err != nil {
			return nil, err
		}

		if out.Code != 0 {
			return nil, fmt.Errorf("list failed: code=%d msg=%s", out.Code, out.Msg)
		}

		allFiles = append(allFiles, out.Data.List...)

		if len(out.Data.List) < d.PageSize {
			break
		}
		pageNum++
	}

	files := make([]model.Object, 0, len(allFiles))
	for _, fi := range allFiles {
		files = append(files, &fi)
	}

	return files, nil
}

func (d *GuangYaPan) Create(ctx context.Context, dir model.PathInfo, args model.CreateArgs) (model.Object, error) {
	parentID := dir.GetID()
	if parentID == "" {
		parentID = "0"
	}

	var out createDirResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/create_file", map[string]any{
		"fileName":     args.Name,
		"parentId":     parentID,
		"resType":      1,
		"verification": "",
	}, &out); err != nil {
		return nil, err
	}

	if out.Code != 0 {
		return nil, fmt.Errorf("create dir failed: code=%d msg=%s", out.Code, out.Msg)
	}

	item := fileItem{
		FileID:   out.Data.FileID,
		ParentID: parentID,
		FileName: out.Data.FileName,
		ResType:  out.Data.ResType,
		CTime:    out.Data.CTime,
		UTime:    out.Data.UTime,
	}
	return &item, nil
}

func (d *GuangYaPan) Move(ctx context.Context, srcObj, dstDir model.PathInfo) error {
	srcID := srcObj.GetID()
	dstParentID := dstDir.GetID()
	if dstParentID == "" {
		dstParentID = "0"
	}

	var out commonResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/move_file", map[string]any{
		"fileIds":      []string{srcID},
		"targetPid":    dstParentID,
		"verification": "",
	}, &out); err != nil {
		return err
	}

	if out.Code != 0 {
		return fmt.Errorf("move failed: code=%d msg=%s", out.Code, out.Msg)
	}
	return nil
}

func (d *GuangYaPan) Rename(ctx context.Context, srcObj model.PathInfo, newName string) error {
	srcID := srcObj.GetID()

	var out commonResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/rename_file", map[string]any{
		"fileId":       srcID,
		"newFileName":  newName,
		"verification": "",
	}, &out); err != nil {
		return err
	}

	if out.Code != 0 {
		return fmt.Errorf("rename failed: code=%d msg=%s", out.Code, out.Msg)
	}
	return nil
}

func (d *GuangYaPan) Copy(ctx context.Context, srcObj, dstDir model.PathInfo) error {
	srcID := srcObj.GetID()
	dstParentID := dstDir.GetID()
	if dstParentID == "" {
		dstParentID = "0"
	}

	var out commonResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/copy_file", map[string]any{
		"fileIds":      []string{srcID},
		"targetPid":    dstParentID,
		"verification": "",
	}, &out); err != nil {
		return err
	}

	if out.Code != 0 {
		return fmt.Errorf("copy failed: code=%d msg=%s", out.Code, out.Msg)
	}
	return nil
}

func (d *GuangYaPan) Remove(ctx context.Context, obj model.PathInfo) error {
	fileID := obj.GetID()

	var out deleteResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/delete_file", map[string]any{
		"fileIds": []string{fileID},
	}, &out); err != nil {
		return err
	}

	if out.Code != 0 {
		return fmt.Errorf("delete failed: code=%d msg=%s", out.Code, out.Msg)
	}

	if err := d.waitTaskDone(ctx, out.Data.TaskID); err != nil {
		return fmt.Errorf("wait delete task done failed: %w", err)
	}
	return nil
}

func (d *GuangYaPan) Put(ctx context.Context, dstDir model.PathInfo, upTask model.UploadTask, stream model.FileStreamer, up driver.UpdateProgress) error {
	parentID := dstDir.GetID()
	if parentID == "" {
		parentID = "0"
	}

	if err := d.validateToken(ctx); err != nil {
		if err := d.ensureAccessToken(ctx); err != nil {
			return err
		}
	}

	uploadToken, code, err := d.getUploadToken(ctx, parentID, stream.GetName(), stream.GetSize())
	if err != nil {
		return fmt.Errorf("get upload token failed: code=%d err=%w", code, err)
	}

	endpoint := normalizeOSSEndpoint(uploadToken.EndPoint, uploadToken.BucketName)
	ossClient, err := oss.New(endpoint, uploadToken.AccessKeyID, uploadToken.SecretAccessKey, oss.SecurityToken(uploadToken.SessionToken))
	if err != nil {
		return fmt.Errorf("create oss client failed: %w", err)
	}

	bucket, err := ossClient.Bucket(uploadToken.BucketName)
	if err != nil {
		return fmt.Errorf("get oss bucket failed: %w", err)
	}

	objectPath := uploadToken.ObjectPath

	if stream.GetSize() <= 100*1024*1024 {
		return d.singleUpload(ctx, bucket, objectPath, stream, up)
	}
	return d.multipartUploadToOSS(ctx, bucket, objectPath, stream, up)
}

func (d *GuangYaPan) singleUpload(ctx context.Context, bucket *oss.Bucket, objectPath string, stream model.FileStreamer, up driver.UpdateProgress) error {
	if err := bucket.PutObject(objectPath, stream, oss.WithContext(ctx)); err != nil {
		return fmt.Errorf("upload to oss failed: %w", err)
	}
	up(100)
	return nil
}

func (d *GuangYaPan) GetDownload(ctx context.Context, path model.PathInfo) (*model.Link, error) {
	fileID := path.GetID()

	var out downloadResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_download_url", map[string]any{
		"fileId": fileID,
	}, &out); err != nil {
		return nil, err
	}

	if out.Code != 0 {
		return nil, fmt.Errorf("get download url failed: code=%d msg=%s", out.Code, out.Msg)
	}

	signedURL := strings.TrimSpace(out.Data.SignedURL)
	if signedURL == "" {
		signedURL = strings.TrimSpace(out.Data.DownloadURL)
	}

	if signedURL == "" {
		return nil, errors.New("download url is empty")
	}

	u, err := url.Parse(signedURL)
	if err != nil {
		return nil, fmt.Errorf("parse signed url failed: %w", err)
	}

	u.RawQuery = strings.ReplaceAll(u.RawQuery, "x-oss-expires=", "x-oss-expires=")

	link := &model.Link{
		URL: u.String(),
	}
	return link, nil
}

func (d *GuangYaPan) GetThumb(ctx context.Context, path model.PathInfo, args model.ThumbArgs) (*model.Link, error) {
	fileID := path.GetID()

	var out downloadResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_download_url", map[string]any{
		"fileId":   fileID,
		"resType":  2,
		"isStream": true,
	}, &out); err != nil {
		return nil, err
	}

	if out.Code != 0 {
		return nil, fmt.Errorf("get thumb url failed: code=%d msg=%s", out.Code, out.Msg)
	}

	signedURL := strings.TrimSpace(out.Data.SignedURL)
	if signedURL == "" {
		signedURL = strings.TrimSpace(out.Data.DownloadURL)
	}
	if signedURL == "" {
		return nil, errors.New("thumb url is empty")
	}

	link := &model.Link{
		URL: signedURL,
	}
	return link, nil
}

func (d *GuangYaPan) ensureAccessToken(ctx context.Context) error {
	if strings.TrimSpace(d.AccessToken) != "" {
		if err := d.validateToken(ctx); err == nil {
			return nil
		}
	}
	if strings.TrimSpace(d.RefreshToken) == "" {
		if d.canSMSLogin() {
			return d.loginBySMSCode(ctx)
		}
		if d.PhoneNumber != "" {
			return errors.New("not logged in yet: please fill verify_code and save storage to finish SMS login")
		}
		return errors.New("access token is empty")
	}
	return d.refreshToken(ctx)
}

func (d *GuangYaPan) validateToken(ctx context.Context) error {
	var me userMeResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetResult(&me).
		Get("/v1/user/me")
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("validate token failed: status=%d body=%s", resp.StatusCode(), resp.String())
	}
	if strings.TrimSpace(me.Sub) == "" {
		return errors.New("validate token failed: empty user sub")
	}
	return nil
}

func (d *GuangYaPan) refreshToken(ctx context.Context) error {
	if strings.TrimSpace(d.RefreshToken) == "" {
		return errors.New("refresh_token is empty")
	}

	var out tokenResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"client_id":     d.ClientID,
			"grant_type":    "refresh_token",
			"refresh_token": d.RefreshToken,
		}).
		SetResult(&out).
		Post("/v1/auth/token")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.AccessToken) == "" {
		errMsg := strings.TrimSpace(out.ErrorDesc)
		if errMsg == "" {
			errMsg = strings.TrimSpace(out.Error)
		}
		if errMsg == "" {
			errMsg = strings.TrimSpace(resp.String())
		}
		if errMsg == "" {
			errMsg = fmt.Sprintf("status=%d", resp.StatusCode())
		}
		return fmt.Errorf("refresh token failed: %s", errMsg)
	}

	d.AccessToken = strings.TrimSpace(out.AccessToken)
	if strings.TrimSpace(out.RefreshToken) != "" {
		d.RefreshToken = strings.TrimSpace(out.RefreshToken)
	}
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *GuangYaPan) canSMSLogin() bool {
	return d.PhoneNumber != "" && d.VerifyCode != ""
}

func (d *GuangYaPan) loginBySMSCode(ctx context.Context) error {
	verificationID := strings.TrimSpace(d.VerificationID)
	if verificationID == "" {
		var err error
		verificationID, err = d.requestVerificationID(ctx)
		if err != nil {
			return err
		}
	}

	var step2 verifyResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"verification_id":   verificationID,
			"verification_code": d.VerifyCode,
			"client_id":         d.ClientID,
		}).
		SetResult(&step2).
		Post("/v1/auth/verification/verify")
	if err != nil {
		return err
	}
	if resp.IsError() || step2.Error != "" || strings.TrimSpace(step2.VerificationToken) == "" {
		return fmt.Errorf("verify code failed: %s", d.accountErr(step2.ErrorDesc, step2.Error, resp))
	}

	var out tokenResp
	resp, err = d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"verification_code":  d.VerifyCode,
			"verification_token": step2.VerificationToken,
			"username":           normalizePhoneE164(d.PhoneNumber),
			"client_id":          d.ClientID,
		}).
		SetResult(&out).
		Post("/v1/auth/signin")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.AccessToken) == "" {
		return fmt.Errorf("signin failed: %s", d.accountErr(out.ErrorDesc, out.Error, resp))
	}

	d.AccessToken = strings.TrimSpace(out.AccessToken)
	d.RefreshToken = strings.TrimSpace(out.RefreshToken)
	d.VerificationID = ""
	d.VerifyCode = ""
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *GuangYaPan) prepareSMSCode(ctx context.Context) error {
	d.VerificationID = ""
	if err := d.ensureCaptchaToken(ctx, false); err != nil {
		return err
	}
	verificationID, err := d.requestVerificationID(ctx)
	if err != nil {
		return err
	}
	d.VerificationID = verificationID
	d.SendCode = false
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *GuangYaPan) requestVerificationID(ctx context.Context) (string, error) {
	if d.CaptchaToken != "" {
		d.accountClient.SetHeader("X-Captcha-Token", d.CaptchaToken)
	}

	var step1 verificationResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"phone_number": normalizePhoneE164(d.PhoneNumber),
			"target":       "ANY",
			"client_id":    d.ClientID,
		}).
		SetResult(&step1).
		Post("/v1/auth/verification")
	if err != nil {
		return "", err
	}
	if resp.IsError() || step1.Error != "" || strings.TrimSpace(step1.VerificationID) == "" {
		if strings.Contains(step1.Error, "captcha_invalid") || strings.Contains(step1.ErrorDesc, "captcha_token expired") {
			if err := d.ensureCaptchaToken(ctx, true); err == nil {
				return d.requestVerificationID(ctx)
			}
		}
		return "", fmt.Errorf("request verification failed: %s", d.accountErr(step1.ErrorDesc, step1.Error, resp))
	}
	return strings.TrimSpace(step1.VerificationID), nil
}

func (d *GuangYaPan) ensureCaptchaToken(ctx context.Context, force bool) error {
	if !force && d.CaptchaToken != "" {
		d.accountClient.SetHeader("X-Captcha-Token", d.CaptchaToken)
		return nil
	}

	var out captchaInitResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"client_id": d.ClientID,
			"action":    "POST:/v1/auth/verification",
			"device_id": d.DeviceID,
			"meta": map[string]any{
				"username":           normalizePhoneE164(d.PhoneNumber),
				"phone_number":       normalizePhoneE164(d.PhoneNumber),
				"VERIFICATION_PHONE": normalizePhoneE164(d.PhoneNumber),
			},
		}).
		SetResult(&out).
		Post("/v1/shield/captcha/init")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.CaptchaToken) == "" {
		return fmt.Errorf("init captcha token failed: %s", d.accountErr(out.ErrorDesc, out.Error, resp))
	}
	d.CaptchaToken = strings.TrimSpace(out.CaptchaToken)
	d.accountClient.SetHeader("X-Captcha-Token", d.CaptchaToken)
	op.MustSaveDriverStorage(d)
	return nil
}

func normalizeCaptchaUsername(phone string) string {
	p := strings.TrimSpace(phone)
	p = strings.ReplaceAll(p, " ", "")
	p = strings.TrimPrefix(p, "+")
	b := make([]rune, 0, len(p))
	for _, ch := range p {
		if ch >= '0' && ch <= '9' {
			b = append(b, ch)
		}
	}
	digits := string(b)
	if strings.HasPrefix(digits, "86") && len(digits) > 11 {
		digits = digits[2:]
	}
	return digits
}

func normalizePhoneE164(phone string) string {
	p := strings.TrimSpace(phone)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, " ", "")
	if strings.HasPrefix(p, "+") {
		if strings.HasPrefix(p, "+86") && len(p) > 3 {
			rest := strings.TrimPrefix(p, "+86")
			return "+86 " + rest
		}
		return p
	}
	digits := normalizeCaptchaUsername(p)
	if len(digits) == 11 {
		return "+86 " + digits
	}
	return p
}

func (d *GuangYaPan) setTempStatus(status string) {
	time.AfterFunc(200*time.Millisecond, func() {
		d.GetStorage().SetStatus(status)
		op.MustSaveDriverStorage(d)
	})
}

func (d *GuangYaPan) accountErr(desc, short string, resp *resty.Response) string {
	msg := strings.TrimSpace(desc)
	if msg == "" {
		msg = strings.TrimSpace(short)
	}
	if msg == "" && resp != nil {
		msg = strings.TrimSpace(resp.String())
	}
	if msg == "" && resp != nil {
		msg = fmt.Sprintf("status=%d", resp.StatusCode())
	}
	if msg == "" {
		msg = "unknown error"
	}
	return msg
}

func (d *GuangYaPan) postAPI(ctx context.Context, path string, body any, out any) error {
	if strings.TrimSpace(d.AccessToken) == "" {
		return errors.New("access token is empty")
	}
	resp, err := d.apiClient.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetBody(body).
		SetResult(out).
		Post(path)
	if err != nil {
		return err
	}
	if resp.StatusCode() == 401 || resp.StatusCode() == 403 {
		if strings.TrimSpace(d.RefreshToken) == "" {
			return fmt.Errorf("request failed: status=%d body=%s", resp.StatusCode(), resp.String())
		}
		if err := d.refreshToken(ctx); err != nil {
			return err
		}
		resp, err = d.apiClient.R().
			SetContext(ctx).
			SetHeader("Authorization", "Bearer "+d.AccessToken).
			SetBody(body).
			SetResult(out).
			Post(path)
		if err != nil {
			return err
		}
	}
	if resp.IsError() {
		return fmt.Errorf("request failed: status=%d body=%s", resp.StatusCode(), resp.String())
	}
	return nil
}

func (d *GuangYaPan) waitTaskDone(ctx context.Context, taskID string) error {
	const (
		maxTry   = 30
		interval = 300 * time.Millisecond
	)
	for i := 0; i < maxTry; i++ {
		var out taskStatusResp
		if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_task_status", map[string]any{
			"taskId": taskID,
		}, &out); err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(out.Msg), "success") {
			return fmt.Errorf("get task status failed: %s", strings.TrimSpace(out.Msg))
		}
		switch out.Data.Status {
		case 2:
			return nil
		case -1, 3:
			return fmt.Errorf("task %s failed with status=%d", taskID, out.Data.Status)
		}
		if i == maxTry-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("task %s timeout", taskID)
}

func (d *GuangYaPan) getUploadToken(ctx context.Context, parentID, name string, size int64) (*uploadTokenData, int, error) {
	var out uploadTokenResp
	err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_res_center_token", map[string]any{
		"capacity": 2,
		"name":     name,
		"parentId": parentID,
		"res": map[string]any{
			"fileSize": size,
		},
	}, &out)
	if err != nil {
		return nil, 0, err
	}
	msg := strings.TrimSpace(out.Msg)
	if msg != "" && !strings.EqualFold(msg, "success") {
		return nil, out.Code, fmt.Errorf("get upload token failed: %s", msg)
	}
	if out.Data.TaskID == "" {
		return nil, out.Code, errors.New("get upload token failed: empty task id")
	}
	if out.Data.AccessKeyID == "" {
		out.Data.AccessKeyID = out.Data.Creds.AccessKeyID
	}
	if out.Data.SecretAccessKey == "" {
		out.Data.SecretAccessKey = out.Data.Creds.SecretAccessKey
	}
	if out.Data.SessionToken == "" {
		out.Data.SessionToken = out.Data.Creds.SessionToken
	}
	if strings.TrimSpace(out.Data.EndPoint) == "" {
		out.Data.EndPoint = strings.TrimSpace(out.Data.FullEndPoint)
	}
	if strings.TrimSpace(out.Data.EndPoint) != "" && !strings.HasPrefix(out.Data.EndPoint, "http://") && !strings.HasPrefix(out.Data.EndPoint, "https://") {
		if strings.TrimSpace(out.Data.FullEndPoint) != "" {
			out.Data.EndPoint = strings.TrimSpace(out.Data.FullEndPoint)
		} else if strings.TrimSpace(out.Data.BucketName) != "" {
			host := strings.TrimSpace(out.Data.EndPoint)
			prefix := strings.TrimSpace(out.Data.BucketName) + "."
			if strings.HasPrefix(host, prefix) {
				out.Data.EndPoint = "https://" + host
			} else {
				out.Data.EndPoint = "https://" + strings.TrimSpace(out.Data.BucketName) + "." + host
			}
		} else {
			out.Data.EndPoint = "https://" + strings.TrimSpace(out.Data.EndPoint)
		}
	}
	return &out.Data, out.Code, nil
}

func (d *GuangYaPan) waitUploadTaskInfo(ctx context.Context, taskID string) error {
	const (
		maxTry   = 300
		interval = 1 * time.Second
	)
	for i := 0; i < maxTry; i++ {
		var out taskInfoResp
		if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/get_info_by_task_id", map[string]any{
			"taskId": taskID,
		}, &out); err != nil {
			return err
		}
		if out.Data.FileID != "" {
			return nil
		}
		switch out.Code {
		case 145, 146, 147, 155, 163, 0:
		default:
			if strings.TrimSpace(out.Msg) != "" {
				return fmt.Errorf("upload task failed: code=%d msg=%s", out.Code, strings.TrimSpace(out.Msg))
			}
		}
		if i == maxTry-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("upload task %s timeout", taskID)
}

func (d *GuangYaPan) multipartUploadToOSS(ctx context.Context, bucket *oss.Bucket, objectPath string, file model.FileStreamer, up driver.UpdateProgress) error {
	partSize := calcUploadPartSize(file.GetSize())
	imur, err := bucket.InitiateMultipartUpload(objectPath, oss.Sequential())
	if err != nil {
		return err
	}

	total := file.GetSize()
	partCount := int((total + partSize - 1) / partSize)
	parts := make([]oss.UploadPart, 0, partCount)
	var uploaded int64
	partNumber := 1

	for uploaded < total {
		if err := ctx.Err(); err != nil {
			return err
		}
		curPartSize := partSize
		left := total - uploaded
		if left < curPartSize {
			curPartSize = left
		}

		reader := io.LimitReader(file, curPartSize)
		part, err := bucket.UploadPart(imur, driver.NewLimitedUploadStream(ctx, reader), curPartSize, partNumber)
		if err != nil {
			return err
		}
		parts = append(parts, part)
		uploaded += curPartSize
		partNumber++
		if total > 0 {
			up(100 * float64(uploaded) / float64(total))
		}
	}

	_, err = bucket.CompleteMultipartUpload(imur, parts)
	return err
}

func calcUploadPartSize(size int64) int64 {
	const (
		mb = int64(1024 * 1024)
		gb = int64(1024 * 1024 * 1024)
	)
	switch {
	case size <= 100*mb:
		return 1 * mb
	case size <= 16*gb:
		return 2 * mb
	case size <= 160*gb:
		return 4 * mb
	default:
		return 8 * mb
	}
}

func normalizeOSSEndpoint(endpoint, bucket string) string {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return ep
	}
	if !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" {
		return ep
	}
	host := u.Host
	prefix := strings.TrimSpace(bucket)
	if prefix != "" && strings.HasPrefix(host, prefix+".") {
		host = strings.TrimPrefix(host, prefix+".")
	}
	u.Host = host
	return u.String()
}

func normalizeDeviceID(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "")
	if len(v) != 32 {
		return ""
	}
	for _, ch := range v {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ""
		}
	}
	return v
}

func randomDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "0123456789abcdef0123456789abcdef"
	}
	return hex.EncodeToString(b)
}

var _ driver.Driver = (*GuangYaPan)(nil)
var _ driver.GetRooter = (*GuangYaPan)(nil)
