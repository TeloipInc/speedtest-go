package speedtest

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

type downloadWarmUpFunc func(context.Context, *http.Client, ServerType, string, int) error
type downloadFunc func(context.Context, *http.Client, ServerType, ProgressUpdater, string, int) error
type uploadWarmUpFunc func(context.Context, *http.Client, ServerType, string, int) error
type uploadFunc func(context.Context, *http.Client, ServerType, ProgressUpdater, string, int) error

// speedtest.net jpg suffixes
var dlSuffixes = [...]int{350, 500, 750, 1000, 1500, 2000, 2500, 3000, 3500, 4000}
var ulSuffixes = [...]int{100, 300, 500, 800, 1000, 1500, 2500, 3000, 3500, 4000} //kB

// overhead compensation factor - 4%
const compensationFactor = 1.04

// SetProgressHandler sets Server's ProgressHandler
func (s *Server) SetProgressHandler(p ProgressUpdater) {
	s.Prog = p
}

// DownloadTest executes the test to measure download speed
func (s *Server) DownloadTest(savingMode bool, duration time.Duration) error {
	return s.downloadTestContext(context.Background(), savingMode, duration, dlWarmUp, downloadRequest)
}

// DownloadTestContext executes the test to measure download speed, observing the given context.
func (s *Server) DownloadTestContext(ctx context.Context, savingMode bool, duration time.Duration) error {
	return s.downloadTestContext(ctx, savingMode, duration, dlWarmUp, downloadRequest)
}

func (s *Server) downloadTestContext(
	ctx context.Context,
	savingMode bool,
	duration time.Duration,
	dlWarmUp downloadWarmUpFunc,
	downloadRequest downloadFunc,
) error {
	eg := errgroup.Group{}

	// choose a suffix that will result in a 1MB file download
	var suffix int
	if s.Type == LibrespeedServer {
		suffix = 1 // MB
	} else {
		suffix = dlSuffixes[2] // jpg axis width
	}

	streams := 10

	// Warming up
	sTime := time.Now()
	for i := 0; i < streams; i++ {
		eg.Go(func() error {
			return dlWarmUp(ctx, s.Doer, s.Type, s.URL, suffix)
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}
	fTime := time.Now()

	// If the bandwidth is too large, the download sometimes finish earlier than the latency.
	// In this case, we ignore the the latency that is included server information.
	// This is not affected to the final result since this is a warm up test.
	timeSpent := fTime.Sub(sTime.Add(s.Latency)).Seconds()
	if timeSpent < 0 {
		timeSpent = fTime.Sub(sTime).Seconds()
	}

	wuSpeed := float64(streams) * suffixToMB(s.Type, suffix) * 8 / timeSpent

	// Decide streams by warm up speed
	skip := false
	weight := 0
	if savingMode {
		streams = 6
		weight = 3
	} else if 50.0 < wuSpeed {
		streams = 32
		weight = 6
	} else if 10.0 < wuSpeed {
		streams = 16
		weight = 4
	} else if 4.0 < wuSpeed {
		streams = 8
		weight = 4
	} else if 2.5 < wuSpeed {
		streams = 4
		weight = 4
	} else {
		skip = true
	}

	if s.Type == LibrespeedServer {
		// calculate the size based on the number of streams and
		// the fact that we need to run the speedtest for duration seconds
		suffix = int(wuSpeed / 8 / float64(streams) * duration.Seconds())
	} else {
		suffix = dlSuffixes[weight]
	}

	// Main speedtest
	dlSpeed := wuSpeed
	if !skip {
		control := make(chan int, streams)

		var totalBytes atomic.Int64
		byteTracker := func(nbytes uint64) {
			totalBytes.Add(int64(nbytes))
			if s.Prog != nil {
				s.Prog.Update(nbytes)
			}
		}

		sTime = time.Now()
		eTime := sTime.Add(duration)
		timedCtx, cancel := context.WithDeadline(ctx, eTime)
		for {
			control <- 747
			if time.Now().After(eTime) || timedCtx.Err() != nil {
				cancel()
				break
			}
			eg.Go(func() error {
				err := downloadRequest(timedCtx, s.Doer, s.Type, ProgressUpdaterFunc(byteTracker), s.URL, suffix)
				<-control
				return err
			})
		}
		eg.Wait()
		fTime = time.Now()
		dlSpeed = float64(totalBytes.Load()/1024/1024) * compensationFactor * 8 / fTime.Sub(sTime).Seconds()
	}

	s.DLSpeed = dlSpeed
	return nil
}

// UploadTest executes the test to measure upload speed
func (s *Server) UploadTest(savingMode bool, duration time.Duration) error {
	return s.uploadTestContext(context.Background(), savingMode, duration, ulWarmUp, uploadRequest)
}

// UploadTestContext executes the test to measure upload speed, observing the given context.
func (s *Server) UploadTestContext(ctx context.Context, savingMode bool, duration time.Duration) error {
	return s.uploadTestContext(ctx, savingMode, duration, ulWarmUp, uploadRequest)
}
func (s *Server) uploadTestContext(
	ctx context.Context,
	savingMode bool,
	duration time.Duration,
	ulWarmUp uploadWarmUpFunc,
	uploadRequest uploadFunc,
) error {
	eg := errgroup.Group{}

	// we need a 1MB file upload
	sizeKB := dlSuffixes[4]

	streams := 2

	// Warm up
	sTime := time.Now()
	for i := 0; i < streams; i++ {
		eg.Go(func() error {
			return ulWarmUp(ctx, s.Doer, s.Type, s.URL, sizeKB)
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}
	fTime := time.Now()

	// 1.0 MB for each request
	wuSpeed := 2 * 1.0 * 8 / fTime.Sub(sTime.Add(s.Latency)).Seconds()

	// Decide streams by warm up speed
	skip := false
	weight := 0
	if savingMode {
		streams = 1
		weight = 7
	} else if 50.0 < wuSpeed {
		streams = 40
		weight = 9
	} else if 10.0 < wuSpeed {
		streams = 16
		weight = 9
	} else if 4.0 < wuSpeed {
		streams = 8
		weight = 9
	} else if 2.5 < wuSpeed {
		streams = 4
		weight = 5
	} else {
		skip = true
	}
	sizeKB = ulSuffixes[weight]

	// Main speedtest
	ulSpeed := wuSpeed
	if !skip {
		control := make(chan int, streams)

		var totalBytes atomic.Int64
		byteTracker := func(nbytes uint64) {
			totalBytes.Add(int64(nbytes))
			if s.Prog != nil {
				s.Prog.Update(nbytes)
			}
		}

		sTime = time.Now()
		eTime := sTime.Add(duration)
		timedCtx, cancel := context.WithDeadline(ctx, eTime)
		for {
			control <- 747
			if time.Now().After(eTime) || timedCtx.Err() != nil {
				cancel()
				break
			}
			eg.Go(func() error {
				err := uploadRequest(timedCtx, s.Doer, s.Type, ProgressUpdaterFunc(byteTracker), s.URL, sizeKB)
				<-control
				return err
			})
		}
		eg.Wait()
		fTime = time.Now()
		ulSpeed = float64(totalBytes.Load()/1024/1024) * compensationFactor * 8 / fTime.Sub(sTime).Seconds()
	}

	s.ULSpeed = ulSpeed
	return nil
}

func dlWarmUp(ctx context.Context, doer *http.Client, serverType ServerType, dlURL string, suffix int) error {
	xdlURL := getDownloadURL(dlURL, serverType, suffix)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xdlURL, nil)
	if err != nil {
		return err
	}

	resp, err := doer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func ulWarmUp(ctx context.Context, doer *http.Client, serverType ServerType, ulURL string, sizeKB int) error {
	xulURL := getUploadURL(ulURL, serverType)

	v := url.Values{}
	v.Add("content", strings.Repeat("0123456789", sizeKB*100-51))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, xulURL, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func downloadRequest(ctx context.Context, doer *http.Client, serverType ServerType, prog ProgressUpdater, dlURL string, suffix int) error {
	xdlURL := getDownloadURL(dlURL, serverType, suffix)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xdlURL, nil)
	if err != nil {
		return err
	}

	resp, err := doer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// install the ProgressHandler
	reader := resp.Body.(io.Reader)
	if prog != nil {
		reader = io.TeeReader(reader, WriterFunc(func(p []byte) (n int, err error) {
			n = len(p)
			if n != 0 {
				prog.Update(uint64(n))
			}
			return n, nil
		}))
	}

	_, err = io.Copy(io.Discard, reader)
	return err
}

func uploadRequest(ctx context.Context, doer *http.Client, serverType ServerType, prog ProgressUpdater, ulURL string, sizeKB int) error {
	xulURL := getUploadURL(ulURL, serverType)

	v := url.Values{}
	v.Add("content", strings.Repeat("0123456789", sizeKB*100-51))

	reqBody := strings.NewReader(v.Encode())

	var body io.Reader
	if prog != nil {
		body = io.TeeReader(reqBody, WriterFunc(func(p []byte) (n int, err error) {
			n = len(p)
			if n != 0 {
				prog.Update(uint64(n))
			}
			return n, nil
		}))
	} else {
		body = reqBody
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, xulURL, body)
	if err != nil {
		return err
	}

	// have to correct the content length and GetBody, due to body type not being one of known ones
	if prog != nil {
		req.ContentLength = int64(reqBody.Len())
		snapshot := *reqBody
		req.GetBody = func() (io.ReadCloser, error) {
			r := snapshot
			return io.NopCloser(&r), nil
		}
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doer.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

// PingTest executes test to measure latency
func (s *Server) PingTest() error {
	return s.PingTestContext(context.Background())
}

// PingTestContext executes test to measure latency, observing the given context.
func (s *Server) PingTestContext(ctx context.Context) error {
	xpingURL := getPingURL(s.URL, s.Type)

	l := time.Second * 10
	for i := 0; i < 3; i++ {
		sTime := time.Now()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, xpingURL, nil)
		if err != nil {
			return err
		}

		resp, err := s.Doer.Do(req)
		if err != nil {
			return err
		}

		fTime := time.Now()
		if fTime.Sub(sTime) < l {
			l = fTime.Sub(sTime)
		}

		resp.Body.Close()
	}

	s.Latency = time.Duration(int64(l.Nanoseconds() / 2))

	return nil
}

func suffixToMB(serverType ServerType, suffix int) float64 {
	if serverType == LibrespeedServer {
		return float64(suffix)
	} else {
		// matching jpg resources found at http://speedtest.tds.net/speedtest/
		return float64(suffix) * float64(suffix) * 2 / 1000 / 1000
	}
}

func getDownloadURL(serverUrl string, serverType ServerType, suffix int) string {
	if serverType == SpeedtestServer {
		// for speedtest.net server url is an upload URL
		baseUrl := strings.Split(serverUrl, "/upload.php")[0]
		return baseUrl + "/random" + strconv.Itoa(suffix) + "x" + strconv.Itoa(suffix) + ".jpg"
	}
	return serverUrl + "/garbage?cors=true&ckSize=" + strconv.Itoa(suffix)
}

func getUploadURL(serverUrl string, serverType ServerType) string {
	if serverType == SpeedtestServer {
		// for speedtest.net server url is an upload URL
		return serverUrl
	}
	return serverUrl + "/empty?cors=true&ckSize=10"
}

func getPingURL(serverUrl string, serverType ServerType) string {
	if serverType == SpeedtestServer {
		// for speedtest.net server url is an upload URL
		baseUrl := strings.Split(serverUrl, "/upload.php")[0]
		return baseUrl + "/latency.txt"
	}
	return serverUrl + "/empty?cors=true&ckSize=1"
}
