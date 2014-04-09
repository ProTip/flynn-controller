package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	ct "github.com/flynn/flynn-controller/types"
	"github.com/flynn/go-discoverd"
	"github.com/flynn/go-discoverd/dialer"
	"github.com/flynn/rpcplus"
	"github.com/flynn/strowger/types"
)

func NewClient(uri string) (*Client, error) {
	if uri == "" {
		uri = "discoverd+http://flynn-controller"
	}
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	c := &Client{
		url:  uri,
		addr: u.Host,
		http: http.DefaultClient,
	}
	if u.Scheme == "discoverd+http" {
		if err := discoverd.Connect(""); err != nil {
			return nil, err
		}
		c.dialer = dialer.New(discoverd.DefaultClient, nil)
		c.http = &http.Client{Transport: &http.Transport{Dial: c.dialer.Dial}}
		u.Scheme = "http"
		c.url = u.String()
	}
	return c, nil
}

type Client struct {
	url  string
	addr string
	http *http.Client

	dialer dialer.Dialer
}

func (c *Client) Close() error {
	return c.dialer.Close()
}

var ErrNotFound = errors.New("controller: not found")

func (c *Client) send(method, path string, in, out interface{}) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, c.url+path, bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return &url.Error{
			Op:  req.Method,
			URL: req.URL.String(),
			Err: fmt.Errorf("controller: unexpected status %d", res.StatusCode),
		}
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

func (c *Client) put(path string, in, out interface{}) error {
	return c.send("PUT", path, in, out)
}

func (c *Client) post(path string, in, out interface{}) error {
	return c.send("POST", path, in, out)
}

func (c *Client) get(path string, out interface{}) error {
	res, err := c.http.Get(c.url + path)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		if res.StatusCode == 404 {
			return ErrNotFound
		}
		return &url.Error{
			Op:  "GET",
			URL: c.url + path,
			Err: fmt.Errorf("controller: unexpected status %d", res.StatusCode),
		}
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (c *Client) StreamFormations(since *time.Time) (<-chan *ct.ExpandedFormation, *error) {
	if since == nil {
		s := time.Unix(0, 0)
		since = &s
	}
	// TODO: handle TLS
	var dial rpcplus.DialFunc
	if c.dialer != nil {
		dial = c.dialer.Dial
	}
	client, err := rpcplus.DialHTTPPath("tcp", c.addr, rpcplus.DefaultRPCPath, dial)
	if err != nil {
		return nil, &err
	}
	ch := make(chan *ct.ExpandedFormation)
	return ch, &client.StreamGo("Controller.StreamFormations", since, ch).Error
}

func (c *Client) CreateArtifact(artifact *ct.Artifact) error {
	return c.post("/artifacts", artifact, artifact)
}

func (c *Client) CreateRelease(release *ct.Release) error {
	return c.post("/releases", release, release)
}

func (c *Client) CreateApp(app *ct.App) error {
	return c.post("/apps", app, app)
}

func (c *Client) CreateProvider(provider *ct.Provider) error {
	return c.post("/providers", provider, provider)
}

func (c *Client) ProvisionResource(req *ct.ResourceReq) (*ct.Resource, error) {
	if req.ProviderID == "" {
		return nil, errors.New("controller: missing provider id")
	}
	res := &ct.Resource{}
	err := c.post(fmt.Sprintf("/providers/%s/resources", req.ProviderID), req, res)
	return res, err
}

func (c *Client) PutResource(resource *ct.Resource) error {
	if resource.ID == "" || resource.ProviderID == "" {
		return errors.New("controller: missing id and/or provider id")
	}
	return c.put(fmt.Sprintf("/providers/%s/resources/%s", resource.ProviderID, resource.ID), resource, resource)
}

func (c *Client) PutFormation(formation *ct.Formation) error {
	if formation.AppID == "" || formation.ReleaseID == "" {
		return errors.New("controller: missing app id and/or release id")
	}
	return c.put(fmt.Sprintf("/apps/%s/formations/%s", formation.AppID, formation.ReleaseID), formation, formation)
}

func (c *Client) SetAppRelease(appID, releaseID string) error {
	return c.put(fmt.Sprintf("/apps/%s/release", appID), &ct.Release{ID: releaseID}, nil)
}

func (c *Client) GetAppRelease(appID string) (*ct.Release, error) {
	release := &ct.Release{}
	return release, c.get(fmt.Sprintf("/apps/%s/release", appID), release)
}

func (c *Client) CreateRoute(appID string, route *strowger.Route) error {
	return c.post(fmt.Sprintf("/apps/%s/routes", appID), route, route)
}

func (c *Client) GetFormation(appID, releaseID string) (*ct.Formation, error) {
	formation := &ct.Formation{}
	return formation, c.get(fmt.Sprintf("/apps/%s/formations/%s", appID, releaseID), formation)
}

func (c *Client) GetRelease(releaseID string) (*ct.Release, error) {
	release := &ct.Release{}
	return release, c.get(fmt.Sprintf("/releases/%s", releaseID), release)
}

func (c *Client) GetArtifact(artifactID string) (*ct.Artifact, error) {
	artifact := &ct.Artifact{}
	return artifact, c.get(fmt.Sprintf("/artifacts/%s", artifactID), artifact)
}

func (c *Client) GetApp(appID string) (*ct.App, error) {
	app := &ct.App{}
	return app, c.get(fmt.Sprintf("/apps/%s", appID), app)
}