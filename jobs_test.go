package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"sort"
	"strings"

	ct "github.com/flynn/flynn-controller/types"
	"github.com/flynn/flynn-controller/utils"
	"github.com/flynn/flynn-host/types"
	"github.com/flynn/go-dockerclient"
	"github.com/flynn/go-flynn/cluster"
	. "github.com/titanous/gocheck"
)

func newFakeCluster() *fakeCluster {
	return &fakeCluster{hostClients: make(map[string]cluster.Host)}
}

type fakeCluster struct {
	hosts       map[string]host.Host
	hostClients map[string]cluster.Host
}

func (c *fakeCluster) ListHosts() (map[string]host.Host, error) {
	return c.hosts, nil
}

func (c *fakeCluster) DialHost(id string) (cluster.Host, error) {
	client, ok := c.hostClients[id]
	if !ok {
		return nil, ErrNotFound
	}
	return client, nil
}

func (c *fakeCluster) AddJobs(req *host.AddJobsReq) (*host.AddJobsRes, error) {
	for hostID, jobs := range req.HostJobs {
		host, ok := c.hosts[hostID]
		if !ok {
			return nil, errors.New("fakeCluster: unknown host")
		}
		host.Jobs = append(host.Jobs, jobs...)
		c.hosts[hostID] = host
	}
	return &host.AddJobsRes{State: c.hosts}, nil
}

func (c *fakeCluster) setHosts(h map[string]host.Host) {
	c.hosts = h
}

func (c *fakeCluster) setHostClient(id string, h cluster.Host) {
	c.hostClients[id] = h
}

func (s *S) TestJobList(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "job-list"})
	s.cc.setHosts(map[string]host.Host{"host0": {
		ID: "host0",
		Jobs: []*host.Job{
			{ID: "job0", Attributes: map[string]string{"flynn-controller.app": app.ID, "flynn-controller.release": "release0", "flynn-controller.type": "web"}},
			{ID: "job1", Attributes: map[string]string{"flynn-controller.app": app.ID}, Config: &docker.Config{Cmd: []string{"bash"}}},
			{ID: "job2", Attributes: map[string]string{"flynn-controller.app": "otherApp"}},
			{ID: "job3"},
		},
	}})

	expected := []ct.Job{
		{ID: "host0-job0", Type: "web", ReleaseID: "release0"},
		{ID: "host0-job1", Cmd: []string{"bash"}},
	}

	var actual []ct.Job
	res, err := s.Get("/apps/"+app.ID+"/jobs", &actual)
	c.Assert(err, IsNil)
	c.Assert(res.StatusCode, Equals, 200)
	c.Assert(actual, DeepEquals, expected)
}

func newFakeHostClient() *fakeHostClient {
	return &fakeHostClient{
		stopped: make(map[string]bool),
		attach:  make(map[string]attachFunc),
	}
}

type fakeHostClient struct {
	stopped map[string]bool
	attach  map[string]attachFunc
}

func (c *fakeHostClient) ListJobs() (map[string]host.ActiveJob, error)                 { return nil, nil }
func (c *fakeHostClient) GetJob(id string) (*host.ActiveJob, error)                    { return nil, nil }
func (c *fakeHostClient) StreamEvents(id string, ch chan<- *host.Event) cluster.Stream { return nil }
func (c *fakeHostClient) Close() error                                                 { return nil }
func (c *fakeHostClient) Attach(req *host.AttachReq, wait bool) (cluster.ReadWriteCloser, func() error, error) {
	f, ok := c.attach[req.JobID]
	if !ok {
		f = c.attach["*"]
	}
	return f(req, wait)
}

func (c *fakeHostClient) StopJob(id string) error {
	c.stopped[id] = true
	return nil
}

func (c *fakeHostClient) isStopped(id string) bool {
	return c.stopped[id]
}

func (c *fakeHostClient) setAttach(id string, rwc cluster.ReadWriteCloser) {
	c.attach[id] = func(*host.AttachReq, bool) (cluster.ReadWriteCloser, func() error, error) {
		return rwc, nil, nil
	}
}

func (c *fakeHostClient) setAttachFunc(id string, f attachFunc) {
	c.attach[id] = f
}

type attachFunc func(req *host.AttachReq, wait bool) (cluster.ReadWriteCloser, func() error, error)

func newFakeLog(r io.Reader) *fakeLog {
	return &fakeLog{r}
}

type fakeLog struct {
	io.Reader
}

func (l *fakeLog) Close() error      { return nil }
func (l *fakeLog) CloseWrite() error { return nil }
func (l *fakeLog) Write([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func (s *S) TestKillJob(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "killjob"})
	hc := newFakeHostClient()
	hostID, jobID := utils.UUID(), utils.UUID()
	s.cc.setHostClient(hostID, hc)

	res, err := s.Delete("/apps/" + app.ID + "/jobs/" + hostID + "-" + jobID)
	c.Assert(err, IsNil)
	c.Assert(res.StatusCode, Equals, 200)
	c.Assert(hc.isStopped(jobID), Equals, true)
}

func (s *S) TestJobLog(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "joblog"})
	hc := newFakeHostClient()
	hostID, jobID := utils.UUID(), utils.UUID()
	hc.setAttach(jobID, newFakeLog(strings.NewReader("foo")))
	s.cc.setHostClient(hostID, hc)

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/apps/%s/jobs/%s-%s/log", s.srv.URL, app.ID, hostID, jobID), nil)
	c.Assert(err, IsNil)
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	var buf bytes.Buffer
	_, err = buf.ReadFrom(res.Body)
	res.Body.Close()
	c.Assert(err, IsNil)

	c.Assert(buf.String(), Equals, "foo")
}

func (s *S) TestJobLogSSE(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "joblog-sse"})
	hc := newFakeHostClient()
	hostID, jobID := utils.UUID(), utils.UUID()
	logData, err := base64.StdEncoding.DecodeString("AQAAAAAAABNMaXN0ZW5pbmcgb24gNTUwMDcKAQAAAAAAAA1oZWxsbyBzdGRvdXQKAgAAAAAAAA1oZWxsbyBzdGRlcnIK")
	c.Assert(err, IsNil)
	hc.setAttach(jobID, newFakeLog(bytes.NewReader(logData)))
	s.cc.setHostClient(hostID, hc)

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/apps/%s/jobs/%s-%s/log", s.srv.URL, app.ID, hostID, jobID), nil)
	c.Assert(err, IsNil)
	req.SetBasicAuth("", authKey)
	req.Header.Set("Accept", "text/event-stream")
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)

	var buf bytes.Buffer
	_, err = buf.ReadFrom(res.Body)
	res.Body.Close()
	c.Assert(err, IsNil)

	expected := "data: {\"stream\":\"stdout\",\"data\":\"Listening on 55007\\n\"}\n\ndata: {\"stream\":\"stdout\",\"data\":\"hello stdout\\n\"}\n\ndata: {\"stream\":\"stderr\",\"data\":\"hello stderr\\n\"}\n\nevent: eof\ndata: {}\n\n"

	c.Assert(buf.String(), Equals, expected)
}

type fakeAttachStream struct {
	io.Reader
	io.WriteCloser
}

func (l *fakeAttachStream) CloseWrite() error { return l.WriteCloser.Close() }
func (l *fakeAttachStream) Close() error      { return l.CloseWrite() }

func (s *S) TestRunJobDetached(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "run-detached"})

	hostID := utils.UUID()
	s.cc.setHosts(map[string]host.Host{hostID: host.Host{}})

	artifact := s.createTestArtifact(c, &ct.Artifact{Type: "docker", URI: "docker://foo/bar"})
	release := s.createTestRelease(c, &ct.Release{
		ArtifactID: artifact.ID,
		Env:        map[string]string{"RELEASE": "true", "FOO": "bar"},
	})

	cmd := []string{"foo", "bar"}
	req := &ct.NewJob{
		ReleaseID: release.ID,
		Cmd:       cmd,
		Env:       map[string]string{"JOB": "true", "FOO": "baz"},
	}
	res := &ct.Job{}
	_, err := s.Post(fmt.Sprintf("/apps/%s/jobs", app.ID), req, res)
	c.Assert(err, IsNil)
	c.Assert(res.ID, Not(Equals), "")
	c.Assert(res.ReleaseID, Equals, release.ID)
	c.Assert(res.Type, Equals, "")
	c.Assert(res.Cmd, DeepEquals, cmd)

	job := s.cc.hosts[hostID].Jobs[0]
	c.Assert(res.ID, Equals, hostID+"-"+job.ID)
	c.Assert(job.Attributes, DeepEquals, map[string]string{
		"flynn-controller.app":     app.ID,
		"flynn-controller.release": release.ID,
	})
	c.Assert(job.Config.Cmd, DeepEquals, []string{"foo", "bar"})
	sort.Strings(job.Config.Env)
	c.Assert(job.Config.Env, DeepEquals, []string{"FOO=baz", "JOB=true", "RELEASE=true"})
	c.Assert(job.Config.AttachStdout, Equals, true)
	c.Assert(job.Config.AttachStderr, Equals, true)
	c.Assert(job.Config.AttachStdin, Equals, false)
	c.Assert(job.Config.StdinOnce, Equals, false)
	c.Assert(job.Config.OpenStdin, Equals, false)
}

func (s *S) TestRunJobAttached(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "run-attached"})
	hc := newFakeHostClient()

	hostID := utils.UUID()
	done := make(chan struct{})
	var jobID string
	hc.setAttachFunc("*", func(req *host.AttachReq, wait bool) (cluster.ReadWriteCloser, func() error, error) {
		c.Assert(wait, Equals, true)
		c.Assert(req.JobID, Not(Equals), "")
		c.Assert(req, DeepEquals, &host.AttachReq{
			JobID:  req.JobID,
			Flags:  host.AttachFlagStdout | host.AttachFlagStderr | host.AttachFlagStdin | host.AttachFlagStream,
			Height: 20,
			Width:  10,
		})
		jobID = req.JobID
		piper, pipew := io.Pipe()
		go func() {
			stdin, err := ioutil.ReadAll(piper)
			c.Assert(err, IsNil)
			c.Assert(string(stdin), Equals, "test in")
			close(done)
		}()
		return &fakeAttachStream{strings.NewReader("test out"), pipew}, func() error { return nil }, nil
	})

	s.cc.setHostClient(hostID, hc)
	s.cc.setHosts(map[string]host.Host{hostID: host.Host{}})

	artifact := s.createTestArtifact(c, &ct.Artifact{Type: "docker", URI: "docker://foo/bar"})
	release := s.createTestRelease(c, &ct.Release{
		ArtifactID: artifact.ID,
		Env:        map[string]string{"RELEASE": "true", "FOO": "bar"},
	})

	data, _ := json.Marshal(&ct.NewJob{
		ReleaseID: release.ID,
		Cmd:       []string{"foo", "bar"},
		Env:       map[string]string{"JOB": "true", "FOO": "baz"},
		TTY:       true,
		Columns:   10,
		Lines:     20,
	})
	req, err := http.NewRequest("POST", s.srv.URL+"/apps/"+app.ID+"/jobs", bytes.NewBuffer(data))
	c.Assert(err, IsNil)
	req.SetBasicAuth("", authKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.flynn.attach")
	_, rwc, err := utils.HijackRequest(req, nil)
	c.Assert(err, IsNil)

	_, err = rwc.Write([]byte("test in"))
	c.Assert(err, IsNil)
	rwc.CloseWrite()
	stdout, err := ioutil.ReadAll(rwc)
	c.Assert(err, IsNil)
	c.Assert(string(stdout), Equals, "test out")
	rwc.Close()

	job := s.cc.hosts[hostID].Jobs[0]
	c.Assert(job.ID, Equals, jobID)
	c.Assert(job.Attributes, DeepEquals, map[string]string{
		"flynn-controller.app":     app.ID,
		"flynn-controller.release": release.ID,
	})
	c.Assert(job.Config.Cmd, DeepEquals, []string{"foo", "bar"})
	sort.Strings(job.Config.Env)
	c.Assert(job.Config.Env, DeepEquals, []string{"FOO=baz", "JOB=true", "RELEASE=true"})
	c.Assert(job.Config.AttachStdout, Equals, true)
	c.Assert(job.Config.AttachStderr, Equals, true)
	c.Assert(job.Config.AttachStdin, Equals, true)
	c.Assert(job.Config.StdinOnce, Equals, true)
	c.Assert(job.Config.OpenStdin, Equals, true)
}
