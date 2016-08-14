package crawler

import (
	"math/rand"
	"time"

	"github.com/henrylee2cn/pholcus/app/downloader"
	"github.com/henrylee2cn/pholcus/app/downloader/request"
	"github.com/henrylee2cn/pholcus/app/pipeline"
	"github.com/henrylee2cn/pholcus/app/spider"
	"github.com/henrylee2cn/pholcus/logs"
	"github.com/henrylee2cn/pholcus/runtime/cache"
)

// 采集引擎
type (
	Crawler interface {
		Init(*spider.Spider) Crawler //初始化采集引擎
		Run()                        //运行任务
		Stop()                       //主动终止
		GetId() int                  //获取引擎ID
	}
	crawler struct {
		*spider.Spider                 //执行的采集规则
		downloader.Downloader          //全局公用的下载器
		pipeline.Pipeline              //结果收集与输出管道
		id                    int      //引擎ID
		pause                 [2]int64 //[请求间隔的最短时长,请求间隔的增幅时长]
	}
)

func New(id int) Crawler {
	return &crawler{
		id:         id,
		Pipeline:   pipeline.New(),
		Downloader: downloader.SurferDownloader,
	}
}

func (self *crawler) Init(sp *spider.Spider) Crawler {
	self.Spider = sp.ReqmatrixInit()
	self.Pipeline.Init(sp)
	self.pause[0] = cache.Task.Pausetime / 2
	if self.pause[0] > 0 {
		self.pause[1] = self.pause[0] * 3
	} else {
		self.pause[1] = 1
	}
	return self
}

// 任务执行入口
func (self *crawler) Run() {
	// 预先启动数据收集/输出管道
	self.Pipeline.Start()

	// 运行处理协程
	c := make(chan bool)
	go func() {
		self.run()
		close(c)
	}()

	// 启动任务
	self.Spider.Start()

	<-c // 等待处理协程退出

	// 停止数据收集/输出管道
	self.Pipeline.Stop()
}

// 主动终止
func (self *crawler) Stop() {
	// 主动崩溃爬虫运行协程
	self.Spider.Stop()
}

func (self *crawler) run() {
	for {
		// 队列中取出一条请求并处理
		if req := self.GetOne(); req == nil {
			// 停止任务
			if self.Spider.CanStop() {
				break
			}

		} else {
			// 执行请求
			self.UseOne()
			go func(req *request.Request) {
				defer func() {
					self.FreeOne()
				}()
				logs.Log.Debug(" *     Start: %v", req.GetUrl())
				self.Process(req)
			}(req)
		}

		// 随机等待
		self.sleep()
	}

	// 等待处理中的任务完成
	self.Spider.Defer()
}

// core processer
func (self *crawler) Process(req *request.Request) {
	var (
		ctx     = self.Downloader.Download(self.Spider, req) // download page
		downUrl = req.GetUrl()
	)

	if err := ctx.GetError(); err != nil {
		// 返回是否作为新的失败请求被添加至队列尾部
		if self.Spider.DoHistory(req, false) {
			// 统计失败数
			cache.PageFailCount()
		}
		// 提示错误
		logs.Log.Error(" *     Fail  [download][%v]: %v\n", downUrl, err)
		return
	}

	defer func() {
		if err := recover(); err != nil {
			if activeStop, _ := err.(string); activeStop == spider.ACTIVE_STOP {
				return
			}
			// 返回是否作为新的失败请求被添加至队列尾部
			if self.Spider.DoHistory(req, false) {
				// 统计失败数
				cache.PageFailCount()
			}
			// 提示错误
			logs.Log.Error(" *     Panic  [process][%v]: %v\n", downUrl, err)
		}
	}()

	// 过程处理，提炼数据
	ctx.Parse(req.GetRuleName())

	// 处理成功请求记录
	self.Spider.DoHistory(req, true)

	// 统计成功页数
	cache.PageSuccCount()

	// 提示抓取成功
	logs.Log.Informational(" *     Success: %v\n", downUrl)

	// 该条请求文本结果存入pipeline
	for _, item := range ctx.PullItems() {
		self.Pipeline.CollectData(item)
	}
	// 该条请求文件结果存入pipeline
	for _, f := range ctx.PullFiles() {
		self.Pipeline.CollectFile(f)
	}
	// 释放ctx准备复用
	spider.PutContext(ctx)
}

// 常用基础方法
func (self *crawler) sleep() {
	sleeptime := self.pause[0] + rand.Int63n(self.pause[1])
	time.Sleep(time.Duration(sleeptime) * time.Millisecond)
}

// 从调度读取一个请求
func (self *crawler) GetOne() *request.Request {
	return self.Spider.RequestPull()
}

//从调度使用一个资源空位
func (self *crawler) UseOne() {
	self.Spider.RequestUse()
}

//从调度释放一个资源空位
func (self *crawler) FreeOne() {
	self.Spider.RequestFree()
}

func (self *crawler) SetId(id int) {
	self.id = id
}

func (self *crawler) GetId() int {
	return self.id
}
