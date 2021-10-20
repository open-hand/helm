/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package repo // import "helm.sh/helm/v3/pkg/repo"

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"sigs.k8s.io/yaml"

	"github.com/open-hand/helm/pkg/chart/loader"
	"github.com/open-hand/helm/pkg/getter"
	"github.com/open-hand/helm/pkg/helmpath"
	"github.com/open-hand/helm/pkg/provenance"
	"github.com/patrickmn/go-cache"
)

// 创建一个cache对象，默认ttl 3分钟，每3分钟对过期数据进行一次清理
var IndexFileCache = cache.New(3*time.Minute, 3*time.Minute)

var mu = &sync.Mutex{}

// Entry represents a collection of parameters for chart repository
type Entry struct {
	Name                  string `json:"name"`
	URL                   string `json:"url"`
	Username              string `json:"username"`
	Password              string `json:"password"`
	CertFile              string `json:"certFile"`
	KeyFile               string `json:"keyFile"`
	CAFile                string `json:"caFile"`
	InsecureSkipTLSverify bool   `json:"insecure_skip_tls_verify"`
}

// ChartRepository represents a chart repository
type ChartRepository struct {
	Config     *Entry
	ChartPaths []string
	IndexFile  *IndexFile
	Client     getter.Getter
	CachePath  string
}

// NewChartRepository constructs ChartRepository
func NewChartRepository(cfg *Entry, getters getter.Providers) (*ChartRepository, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, errors.Errorf("invalid chart URL format: %s", cfg.URL)
	}

	client, err := getters.ByScheme(u.Scheme)
	if err != nil {
		return nil, errors.Errorf("could not find protocol handler for: %s", u.Scheme)
	}

	return &ChartRepository{
		Config:    cfg,
		IndexFile: NewIndexFile(),
		Client:    client,
		CachePath: helmpath.CachePath("repository"),
	}, nil
}

// Load loads a directory of charts as if it were a repository.
//
// It requires the presence of an index.yaml file in the directory.
func (r *ChartRepository) Load() error {
	dirInfo, err := os.Stat(r.Config.Name)
	if err != nil {
		return err
	}
	if !dirInfo.IsDir() {
		return errors.Errorf("%q is not a directory", r.Config.Name)
	}

	// FIXME: Why are we recursively walking directories?
	// FIXME: Why are we not reading the repositories.yaml to figure out
	// what repos to use?
	filepath.Walk(r.Config.Name, func(path string, f os.FileInfo, err error) error {
		if !f.IsDir() {
			if strings.Contains(f.Name(), "-index.yaml") {
				i, err := LoadIndexFile(path)
				if err != nil {
					return nil
				}
				r.IndexFile = i
			} else if strings.HasSuffix(f.Name(), ".tgz") {
				r.ChartPaths = append(r.ChartPaths, path)
			}
		}
		return nil
	})
	return nil
}

// DownloadIndexFile fetches the index from a repository.
func (r *ChartRepository) DownloadIndexFile() (*IndexFile, string, error) {
	parsedURL, err := url.Parse(r.Config.URL)
	if err != nil {
		return nil, "", err
	}
	parsedURL.RawPath = path.Join(parsedURL.RawPath, "index.yaml")
	parsedURL.Path = path.Join(parsedURL.Path, "index.yaml")

	indexURL := parsedURL.String()
	// TODO add user-agent
	resp, err := r.Client.Get(indexURL,
		getter.WithURL(r.Config.URL),
		getter.WithInsecureSkipVerifyTLS(r.Config.InsecureSkipTLSverify),
		getter.WithTLSClientConfig(r.Config.CertFile, r.Config.KeyFile, r.Config.CAFile),
		getter.WithBasicAuth(r.Config.Username, r.Config.Password),
	)
	if err != nil {
		return nil, "", err
	}

	index, err := ioutil.ReadAll(resp)
	if err != nil {
		return nil, "", err
	}

	indexFile, err := loadIndex(index)
	if err != nil {
		return nil, "", err
	}

	// Create the chart list file in the cache directory
	var charts strings.Builder
	for name := range indexFile.Entries {
		fmt.Fprintln(&charts, name)
	}
	chartsFile := filepath.Join(r.CachePath, helmpath.CacheChartsFile(r.Config.Name))
	os.MkdirAll(filepath.Dir(chartsFile), 0755)
	ioutil.WriteFile(chartsFile, []byte(charts.String()), 0644)

	// Create the index file in the cache directory
	fname := filepath.Join(r.CachePath, helmpath.CacheIndexFile(r.Config.Name))
	os.MkdirAll(filepath.Dir(fname), 0755)
	return indexFile, fname, ioutil.WriteFile(fname, index, 0644)
}

// Index generates an index for the chart repository and writes an index.yaml file.
func (r *ChartRepository) Index() error {
	err := r.generateIndex()
	if err != nil {
		return err
	}
	return r.saveIndexFile()
}

func (r *ChartRepository) saveIndexFile() error {
	index, err := yaml.Marshal(r.IndexFile)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(r.Config.Name, indexPath), index, 0644)
}

func (r *ChartRepository) generateIndex() error {
	for _, path := range r.ChartPaths {
		ch, err := loader.Load(path)
		if err != nil {
			return err
		}

		digest, err := provenance.DigestFile(path)
		if err != nil {
			return err
		}

		if !r.IndexFile.Has(ch.Name(), ch.Metadata.Version) {
			r.IndexFile.Add(ch.Metadata, path, r.Config.URL, digest)
		}
		// TODO: If a chart exists, but has a different Digest, should we error?
	}
	r.IndexFile.SortEntries()
	return nil
}

// FindChartInRepoURL finds chart in chart repository pointed by repoURL
// without adding repo to repositories
func FindChartInRepoURL(repoURL, chartName, chartVersion, certFile, keyFile, caFile string, getters getter.Providers) (string, error) {
	return FindChartInAuthRepoURL(repoURL, "", "", chartName, chartVersion, certFile, keyFile, caFile, getters)
}

// FindChartInAuthRepoURL finds chart in chart repository pointed by repoURL
// without adding repo to repositories, like FindChartInRepoURL,
// but it also receives credentials for the chart repository.
func FindChartInAuthRepoURL(repoURL, username, password, chartName, chartVersion, certFile, keyFile, caFile string, getters getter.Providers) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	var repoIndex *IndexFile
	// 获取缓存中的repoIndex
	value, exist := IndexFileCache.Get(repoURL)
	if !exist {
		// 未命中缓存
		var err error
		repoIndex, err = GetAndCacheIndexFile(repoURL, username, password, certFile, keyFile, caFile, getters)
		if err != nil {
			return "", err
		}
	} else {
		// 命中缓存
		repoIndex = value.(*IndexFile)
	}

	errMsg := fmt.Sprintf("chart %q", chartName)
	if chartVersion != "" {
		errMsg = fmt.Sprintf("%s version %q", errMsg, chartVersion)
	}
	cv, err := repoIndex.Get(chartName, chartVersion)
	// err不为nil，可能是repoIndex数据过旧造成，尝试更新repoIndex后再获取ChartVersion,如果err仍不为nil，返回错误
	if err != nil {
		// 删除旧缓存
		IndexFileCache.Delete(repoURL)
		repoIndex, err = GetAndCacheIndexFile(repoURL, username, password, certFile, keyFile, caFile, getters)
		if err != nil {
			return "", err
		}
		cv, err = repoIndex.Get(chartName, chartVersion)
		if err != nil {
			return "", errors.Errorf("%s not found in %s repository", errMsg, repoURL)
		}
		IndexFileCache.Set(repoURL, IndexFileCache, cache.DefaultExpiration)
	}

	if len(cv.URLs) == 0 {
		return "", errors.Errorf("%s has no downloadable URLs", errMsg)
	}

	chartURL := cv.URLs[0]

	absoluteChartURL, err := ResolveReferenceURL(repoURL, chartURL)
	if err != nil {
		return "", errors.Wrap(err, "failed to make chart URL absolute")
	}

	return absoluteChartURL, nil
}

// ResolveReferenceURL resolves refURL relative to baseURL.
// If refURL is absolute, it simply returns refURL.
func ResolveReferenceURL(baseURL, refURL string) (string, error) {
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return "", errors.Wrapf(err, "failed to parse %s as URL", baseURL)
	}

	parsedRefURL, err := url.Parse(refURL)
	if err != nil {
		return "", errors.Wrapf(err, "failed to parse %s as URL", refURL)
	}

	// We need a trailing slash for ResolveReference to work, but make sure there isn't already one
	parsedBaseURL.Path = strings.TrimSuffix(parsedBaseURL.Path, "/") + "/"
	return parsedBaseURL.ResolveReference(parsedRefURL).String(), nil
}

func GetAndCacheIndexFile(repoURL, username, password, certFile, keyFile, caFile string, getters getter.Providers) (*IndexFile, error) {
	// 如果不存在，从仓库下载index并导入
	// Download and write the index file to a temporary location
	buf := make([]byte, 20)
	rand.Read(buf)
	name := strings.ReplaceAll(base64.StdEncoding.EncodeToString(buf), "/", "-")

	c := Entry{
		URL:      repoURL,
		Username: username,
		Password: password,
		CertFile: certFile,
		KeyFile:  keyFile,
		CAFile:   caFile,
		Name:     name,
	}
	r, err := NewChartRepository(&c, getters)
	if err != nil {
		return nil, err
	}
	repoIndex, _, err := r.DownloadIndexFile()
	if err != nil {
		return nil, errors.Wrapf(err, "looks like %q is not a valid chart repository or cannot be reached", repoURL)
	}

	IndexFileCache.Set(repoURL, repoIndex, cache.DefaultExpiration)
	return repoIndex, nil
}

func (e *Entry) String() string {
	buf, err := json.Marshal(e)
	if err != nil {
		log.Panic(err)
	}
	return string(buf)
}
