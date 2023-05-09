// Copyright 2016 CodisLabs. All Rights Reserved.
// Licensed under the MIT (MIT-LICENSE.txt) license.

package proxy

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CodisLabs/codis/pkg/utils/log"
	"github.com/CodisLabs/codis/pkg/proxy/redis"
	"github.com/CodisLabs/codis/pkg/utils"
	"github.com/CodisLabs/codis/pkg/utils/sync2/atomic2"
)

const TPFirstGrade = 5				//5ms - 200ms
const TPFirstGradeSize = 40
const TPSecondGrade = 25		    //225ms - 700ms
const TPSecondGradeSize = 20
const TPThirdGrade = 250			    //950ms - 3200ms
const TPThirdGradeSize = 10
const TPMaxNum = TPFirstGradeSize + TPSecondGradeSize + TPThirdGradeSize
const ClearSlowFlagPeriodRate = 3	//慢命令清理周期是统计周期的三倍
const IntervalNum = 5
const DelayKindNum = 8
// 单位: s
var IntervalMark = [IntervalNum]int64{1, 10, 60, 600, 3600}
var LastRefreshTime = [IntervalNum]time.Time{time.Now()}
// 单位: ms
var DelayNumMark = [DelayKindNum]int64{50, 100, 200, 300, 500, 1000, 2000, 3000}

type delayInfo struct {
	interval	int64	
	calls 		atomic2.Int64
	nsecs 		atomic2.Int64
	nsecsmax  	atomic2.Int64
	avg 		int64
	qps 		atomic2.Int64

	tp    	[TPMaxNum]atomic2.Int64
	tp90  	int64
	tp99  	int64
	tp999 	int64
	tp9999 	int64
	tp100 	int64

	delayCount   [DelayKindNum]atomic2.Int64
	delay50ms    int64
	delay100ms   int64
	delay200ms   int64
	delay300ms   int64
	delay500ms   int64
	delay1s      int64
	delay2s      int64
	delay3s      int64
}

type opStats struct {
	opstr 		string
	totalCalls 	atomic2.Int64
	totalNsecs 	atomic2.Int64
	totalFails 	atomic2.Int64
	lastSetSlowTime 	int64
	lastClearSlowTime 	int64

	delayInfo    [IntervalNum]*delayInfo

	redis 	struct {
		errors atomic2.Int64
	}
}

type OpStats struct {
	OpStr          string `json:"opstr"`
	Interval       int64  `json:"interval"`
	TotalCalls     int64  `json:"total_calls"`
	TotalUsecs     int64  `json:"total_usecs"`
	UsecsPercall   int64  `json:"usecs_percall"`

	Calls          int64  `json:"calls"`
	Usecs          int64  `json:"usecs"`
	Fails          int64  `json:"fails"`
	RedisErrType   int64  `json:"redis_errtype"`
	QPS 		   int64  `json:"qps"`
	AVG            int64  `json:"avg"`
	TP90  		   int64  `json:"tp90"`
	TP99  		   int64  `json:"tp99"`
	TP999  		   int64  `json:"tp999"`
	TP9999  	   int64  `json:"tp9999"`
	TP100          int64  `json:"tp100"`

	Delay50ms    int64  `json:"delay50ms"`
	Delay100ms   int64  `json:"delay100ms"`
	Delay200ms   int64  `json:"delay200ms"`
	Delay300ms   int64  `json:"delay300ms"`
	Delay500ms   int64  `json:"delay500ms"`
	Delay1s      int64  `json:"delay1s"`
	Delay2s      int64  `json:"delay2s"`
	Delay3s      int64  `json:"delay3s"`
}

var cmdstats struct {
	sync.RWMutex 				//仅仅对opmap进行加锁

	opmap map[string]*opStats
	total atomic2.Int64
	fails atomic2.Int64
	redis struct {
		errors atomic2.Int64
	}

	qps atomic2.Int64
	tpdelay		[TPMaxNum]int64   //us
	refreshPeriod 	atomic2.Int64
	logSlowerThan   atomic2.Int64
	autoSetSlowFlag atomic2.Bool
}

func init() {
	cmdstats.opmap = make(map[string]*opStats, 128)
	cmdstats.refreshPeriod.Set(int64(time.Second))

	//init tp delay array
	for i := 0; i < TPMaxNum; i++ {
		if i < TPFirstGradeSize {
			cmdstats.tpdelay[i] = int64(i + 1) * TPFirstGrade
		} else if i < TPFirstGradeSize + TPSecondGradeSize {
			cmdstats.tpdelay[i] = TPFirstGradeSize * TPFirstGrade + int64(i - TPFirstGradeSize + 1) * TPSecondGrade
		} else {
			cmdstats.tpdelay[i] = TPFirstGradeSize * TPFirstGrade + TPSecondGradeSize * TPSecondGrade  + int64(i - TPFirstGradeSize -  TPSecondGradeSize + 1) * TPThirdGrade
		}
	}

	// init LastRefreshTime array
	for i := 0; i < IntervalNum; i++ {
		LastRefreshTime[i] = time.Now()
	}

	//log.Debugf("cmdstats.tpdelay: %v", cmdstats.tpdelay)

	//周期性设置命令慢标志和清理命令慢标志；
	//将设置和清理操作放到一个协程里面做，防止由于时序问题，命令慢标志被设置后永远无法被清理
	go func() {
		for {
			if cmdstats.refreshPeriod.Int64() <= 0 || cmdstats.autoSetSlowFlag.IsFalse() {
				time.Sleep(time.Second)
				continue
			}

			clearSlowDuration := cmdstats.refreshPeriod.Int64() * ClearSlowFlagPeriodRate

			if cmdstats.refreshPeriod.Int64() <= int64(time.Second) {
				time.Sleep( time.Second )
			} else {
				time.Sleep( time.Duration(cmdstats.refreshPeriod.Int64()) )
			}

			now := time.Now().UnixNano()
			cmdstats.RLock()
			//设置慢标志时，必须判断autoSetSlowFlag条件；防止proxy关闭autoSetSlowFlag后，程序刚好走到这里
			//这种情况下慢标志将永远无法被清理
			//由于tp100最小单位是1ms，因此tp100 >= 1ms时才会生效；
			if cmdstats.autoSetSlowFlag.IsTrue() {
				for _, v := range cmdstats.opmap{
					if v.delayInfo[0].tp100 * 1e3 > cmdstats.logSlowerThan.Int64() && v.opstr != "ALL" {
						setMaySlowOpFlag(v.opstr)
						v.lastSetSlowTime = now
					} else if v.lastSetSlowTime >= v.lastClearSlowTime && now - v.lastSetSlowTime >= clearSlowDuration {
						clearMaySlowOpFlag(v.opstr)
						v.lastClearSlowTime = now
					}
				}
			}
			cmdstats.RUnlock()
		}
	}()

	go func() {
		for {
			if cmdstats.refreshPeriod.Int64() <= 0 {
				time.Sleep(time.Second)
				continue
			}

			start := time.Now()
			total := cmdstats.total.Int64()
			if cmdstats.refreshPeriod.Int64() <= int64(time.Second) {
				time.Sleep(time.Second)
			} else {
				time.Sleep( time.Duration(cmdstats.refreshPeriod.Int64()) )
			}

			delta := cmdstats.total.Int64() - total
			normalized := math.Max(0, float64(delta)) / float64(time.Since(start)) * float64(time.Second) 
			cmdstats.qps.Set(int64(normalized + 0.5))

			cmdstats.RLock()

			for i:=0; i<IntervalNum; i++ {

				if int64(float64(time.Since(LastRefreshTime[i])) / float64(time.Second)) < IntervalMark[i] {
					continue
				}
				for _, v := range cmdstats.opmap{
					v.RefreshOpStats(i)
				}
				LastRefreshTime[i] = time.Now()
			}
			cmdstats.RUnlock()
		}
	}()
}

func (s *delayInfo) refreshTpInfo(cmd string) {
	s.refresh4TpInfo(cmd)
	s.tp100 = s.nsecsmax.Int64() / 1e6

	if calls := s.calls.Int64(); calls != 0 {
		s.avg = s.nsecs.Int64() / 1e6 / calls
	} else {
		s.avg = 0
	}
}

func (s *delayInfo) refresh4TpInfo(cmd string) {
	persents1 := 0.9
	persents2 := 0.99 
	persents3 := 0.999
	persents4 := 0.9999

	if s.calls.Int64() == 0 {
		s.tp90 = 0
		s.tp99 = 0
		s.tp999 = 0
		s.tp9999 = 0
		return
	}

	tpnum1 := int64( float64(s.calls.Int64()) * persents1 )
	tpnum2 := int64( float64(s.calls.Int64()) * persents2 )
	tpnum3 := int64( float64(s.calls.Int64()) * persents3 )
	tpnum4 := int64( float64(s.calls.Int64()) * persents4 )

	var index1, index2, index3, index4 int
	var count int64
	var i 	  int

	for i = 0; i < len(s.tp); i++ {
		count += s.tp[i].Int64()
		if count >= tpnum1 || i == len(s.tp)-1 {
			index1 = i
			break
		}
	}

	if count >= tpnum2 || i == len(s.tp)-1 {
		index2 = i
	} else {
		for i = i+1; i < len(s.tp); i++ {
			count += s.tp[i].Int64()
			if count >= tpnum2 || i == len(s.tp)-1 {
				index2 = i
				break
			}
		}
	}

	if count >= tpnum3 || i == len(s.tp)-1 {
		index3 = i
	} else {
		for i = i+1; i < len(s.tp); i++ {
			count += s.tp[i].Int64()
			if count >= tpnum3 || i == len(s.tp)-1 {
				index3 = i
				break
			}
		}
	}

	if count >= tpnum4 || i == len(s.tp)-1 {
		index4 = i
	} else {
		for i = i+1; i < len(s.tp); i++ {
			count += s.tp[i].Int64()
			if count >= tpnum4 || i == len(s.tp)-1 {
				index4 = i
				break
			}
		}
	}

	// 统计出现异常,打印一行日志
	if i == len(s.tp)-1 && s.tp[i].Int64() <= 0 {
		log.Warnf("refreshTpInfo err: cmd-[%s] tpinfo is unavailable", cmd)
	}

	if index1 >= 0 && index2 >= index1 && index3 >= index2 && index4 >= index3 && index4 < TPMaxNum {
		s.tp90 = cmdstats.tpdelay[index1]
		s.tp99 = cmdstats.tpdelay[index2]
		s.tp999 = cmdstats.tpdelay[index3]
		s.tp9999 = cmdstats.tpdelay[index4]
		return 
	}

	log.Warnf("refreshTpInfo err: cmd-[%s] reset exception tpinf", cmd)
	s.tp90 = -1
	s.tp99 = -1
	s.tp999 = -1
	s.tp9999 = -1
	return	
}

func (s *delayInfo) resetTpInfo() {
	s.calls.Set(0)
	s.nsecs.Set(0)
	s.nsecsmax.Set(0)
	s.tp = [TPMaxNum]atomic2.Int64{0}
}

func (s *delayInfo) refreshDelayInfo() {
	s.delay50ms = s.delayCount[0].Int64()
	s.delay100ms = s.delayCount[1].Int64()
	s.delay200ms = s.delayCount[2].Int64()
	s.delay300ms = s.delayCount[3].Int64()
	s.delay500ms = s.delayCount[4].Int64()
	s.delay1s = s.delayCount[5].Int64()
	s.delay2s = s.delayCount[6].Int64()
	s.delay3s = s.delayCount[7].Int64()
}

func (s *delayInfo) resetDelayInfo() {
	s.delayCount  = [DelayKindNum]atomic2.Int64{0}
}

//IncrTP()中duration单位为ns
func (s *opStats) incrTP(duration int64) {
	var index int64 = -1
	var duration_ms int64 = duration / 1e6
	if duration_ms <= 0 {
		//s.tp[0].Incr()
		index = 0
	}else if duration_ms <= TPFirstGrade*TPFirstGradeSize {
		index = (duration_ms + TPFirstGrade - 1) / TPFirstGrade - 1
		//s.tp[index].Incr()
	} else if duration_ms <= TPFirstGrade*TPFirstGradeSize + TPSecondGrade*TPSecondGradeSize {
		index = (duration_ms - TPFirstGrade*TPFirstGradeSize + TPSecondGrade - 1) / TPSecondGrade + TPFirstGradeSize - 1
		//s.tp[index].Incr()
	} else if duration_ms <= TPFirstGrade*TPFirstGradeSize + TPSecondGrade*TPSecondGradeSize + TPThirdGrade*TPThirdGradeSize {
		index = (duration_ms - TPFirstGrade*TPFirstGradeSize - TPSecondGrade*TPSecondGradeSize + TPThirdGrade - 1) / TPThirdGrade + TPFirstGradeSize + TPSecondGradeSize - 1
		//s.tp[index].Incr()
	} else {
		index = TPMaxNum - 1
		//s.tp[TPMaxNum - 1].Incr()
	}

	if index < 0 {
		return
	}

	for i := 0; i < IntervalNum; i++ {
		s.delayInfo[i].calls.Incr()
		s.delayInfo[i].nsecs.Add(duration)
		lastMax := s.delayInfo[i].nsecsmax.Int64()
		//max值最大误差设置为5ms，防止瞬间有多个线程同时进行更新
		if duration >= lastMax + 5*1e6 {
			for ; ; {
				ok := s.delayInfo[i].nsecsmax.CompareAndSwap(lastMax, duration)
				if ok {
					break;
				} else {
					lastMax = s.delayInfo[i].nsecsmax.Int64()
					if duration < lastMax + 5*1e6 {
						//log.Warnf("CompareAndSwap return false and break, newMax is [%d] lastMax is [%d] now time is [%v], ",duration, lastMax, time.Now())
						break

					}
					log.Warnf("CompareAndSwap return false and try again, newMax is [%d ns] lastMax is [%d ns]",duration, lastMax)
				}
			}
		}
		s.delayInfo[i].tp[index].Incr()
	}
}


//persents support 0 < persents <= 1 only
/*func (s *opStats) GetTP(persents float64) int64{
	if s.calls.Int64() == 0 || persents <= 0 || persents > 1 {
		return 0
	}

	tpnum := int64( float64(s.calls.Int64()) * persents )
	var count int64
	var index int

	for i, v := range s.tp {
		count += v.Int64()
		if count >= tpnum || i == len(s.tp)-1 {
			index = i
			break
		}
	}

	if index >= 0 && index < TPMaxNum {
		return cmdstats.tpdelay[index]
	}

	return -1
}*/

//persents support 0 < persents <= 1 only
/*func (s *opStats) Get4TP(persents1, persents2, persents3, persents4 float64) (int64, int64, int64, int64){
	if s.calls.Int64() == 0 {
		return 0, 0, 0, 0
	}

	if !(persents1 > 0 && persents2 >= persents1 && persents3 >= persents2 && persents4 >= persents3 && persents4 <= 1.0) {
		return -1, -1, -1, -1 
	}

	tpnum1 := int64( float64(s.calls.Int64()) * persents1 )
	tpnum2 := int64( float64(s.calls.Int64()) * persents2 )
	tpnum3 := int64( float64(s.calls.Int64()) * persents3 )
	tpnum4 := int64( float64(s.calls.Int64()) * persents4 )

	var index1, index2, index3, index4 int
	var count int64
	var i 	  int

	for i = 0; i < len(s.tp); i++ {
		count += s.tp[i].Int64()
		if count >= tpnum1 || i == len(s.tp)-1 {
			index1 = i
			break
		}
	}

	if count >= tpnum2 || i == len(s.tp)-1 {
		index2 = i
	} else {
		for i = i+1; i < len(s.tp); i++ {
			count += s.tp[i].Int64()
			if count >= tpnum2 || i == len(s.tp)-1 {
				index2 = i
				break
			}
		}
	}

	if count >= tpnum3 || i == len(s.tp)-1 {
		index3 = i
	} else {
		for i = i+1; i < len(s.tp); i++ {
			count += s.tp[i].Int64()
			if count >= tpnum3 || i == len(s.tp)-1 {
				index3 = i
				break
			}
		}
	}

	if count >= tpnum4 || i == len(s.tp)-1 {
		index4 = i
	} else {
		for i = i+1; i < len(s.tp); i++ {
			count += s.tp[i].Int64()
			if count >= tpnum4 || i == len(s.tp)-1 {
				index4 = i
				break
			}
		}
	}

	if index1 >= 0 && index2 >= index1 && index3 >= index2 && index4 >= index3 && index4 < TPMaxNum {
		return cmdstats.tpdelay[index1], cmdstats.tpdelay[index2], cmdstats.tpdelay[index3], cmdstats.tpdelay[index4]
	}

	return -1, -1, -1, -1
}*/

func (s *opStats) RefreshOpStats(index int) {
	if index < 0 || index >= IntervalNum {
		return
	}
	normalized := math.Max(0, float64(s.delayInfo[index].calls.Int64())) / float64(time.Since(LastRefreshTime[index])) * float64(time.Second)
	s.delayInfo[index].qps.Set(int64(normalized + 0.5))

	s.delayInfo[index].refreshTpInfo(s.opstr)
	s.delayInfo[index].resetTpInfo()

	// 统计超时命令数量
	s.delayInfo[index].refreshDelayInfo()
	s.delayInfo[index].resetDelayInfo()
}

//duration单位为ms
func (s *opStats) incrDelayNum(duration int64) {
	for i, v := range DelayNumMark {
		if duration >= v {
			for j, _ := range IntervalMark {
				s.delayInfo[j].delayCount[i].Incr()
			}
		} else {
			break
		}
	}
}

func (s *opStats) GetOpStatsByInterval(interval int64) *OpStats {
	var index int64 = -1
	var i int64
	for i = 0; i<IntervalNum; i++ {
		if interval == IntervalMark[i] {
			index = i
		}
	}
	if index < 0 {
		index = 0
	}

	o := &OpStats{
		OpStr: s.opstr,
		Interval: s.delayInfo[index].interval,
		TotalCalls: s.totalCalls.Int64(),
		TotalUsecs: s.totalNsecs.Int64() / 1e3,
		Fails: s.totalFails.Int64(),
		Calls: s.delayInfo[index].calls.Int64(),
		Usecs: s.delayInfo[index].nsecs.Int64() / 1e3,
		QPS:   s.delayInfo[index].qps.Int64(),
		AVG:   s.delayInfo[index].avg,
		TP90:  s.delayInfo[index].tp90,
		TP99:  s.delayInfo[index].tp99,
		TP999:   s.delayInfo[index].tp999,
		TP9999:  s.delayInfo[index].tp9999,
		TP100:	 s.delayInfo[index].tp100,
		Delay50ms: s.delayInfo[index].delay50ms,
		Delay100ms: s.delayInfo[index].delay100ms,
		Delay200ms: s.delayInfo[index].delay200ms,
		Delay300ms: s.delayInfo[index].delay300ms,
		Delay500ms: s.delayInfo[index].delay500ms,
		Delay1s: s.delayInfo[index].delay1s,
		Delay2s: s.delayInfo[index].delay2s,
		Delay3s: s.delayInfo[index].delay3s,
	}

	if o.Calls != 0 {
		o.UsecsPercall = o.Usecs / o.Calls
	}
	o.RedisErrType = s.redis.errors.Int64()

	return o
}

func (s *opStats)incrOpStats(responseTime int64, t redis.RespType) {
	s.totalCalls.Incr()
	s.totalNsecs.Add(responseTime)
	switch t {
		case redis.TypeError:
			s.redis.errors.Incr()
	}
	
	//统计tp数据
	s.incrTP( responseTime )
	//统计超时命令数量
	s.incrDelayNum( responseTime/1e6 )
}

func StatsSetRefreshPeriod(d time.Duration) {
	if d >= 0 {
		cmdstats.refreshPeriod.Set( int64(d) )
	}
}

func StatsSetLogSlowerThan(ms int64) {
	if ms >= 0 {
		cmdstats.logSlowerThan.Set( ms )
	}
}

func StatsSetAutoSetSlowFlag(autoset bool) {
	cmdstats.autoSetSlowFlag.Set( autoset )

	//清除已经被设置为慢标志的命令
	//这里使用写锁，防止命令被其他地方设置慢标志，保证慢标志被清理完之后不会再被设置
	if cmdstats.autoSetSlowFlag.IsFalse() {
		cmdstats.Lock()
		for _, v := range cmdstats.opmap{
			clearMaySlowOpFlag(v.opstr)
			log.Infof("StatsSetAutoSetSlowFlag do clean : v.opstr[%s], lastSetSlowTime[%d]ms, lastClearSlowTime[%d]", v.opstr, v.lastSetSlowTime/1e6, v.lastClearSlowTime/1e6)
		}
		cmdstats.Unlock()
	}
}

func OpTotal() int64 {
	return cmdstats.total.Int64()
}

func OpFails() int64 {
	return cmdstats.fails.Int64()
}

func OpRedisErrors() int64 {
	return cmdstats.redis.errors.Int64()
}

func OpQPS() int64 {
	return cmdstats.qps.Int64()
}

func getOpStats(opstr string, create bool) *opStats {
	cmdstats.RLock()
	s := cmdstats.opmap[opstr]
	cmdstats.RUnlock()

	if s != nil || !create {
		return s
	}

	cmdstats.Lock()
	s = cmdstats.opmap[opstr]
	if s == nil {
		s = &opStats{opstr: opstr}
		for i:=0; i<IntervalNum; i++ {
			s.delayInfo[i] = &delayInfo{interval: IntervalMark[i]}
		}
		cmdstats.opmap[opstr] = s
	}
	cmdstats.Unlock()
	return s
}

type sliceOpStats []*OpStats

func (s sliceOpStats) Len() int {
	return len(s)
}

func (s sliceOpStats) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s sliceOpStats) Less(i, j int) bool {
	return s[i].OpStr < s[j].OpStr
}

/*func GetOpStatsAll() []*OpStats {
	var all = make([]*OpStats, 0, 128)
	cmdstats.RLock()
	for _, s := range cmdstats.opmap {
		all = append(all, s.GetOpStatsByInterval(1))
	}
	cmdstats.RUnlock()
	sort.Sort(sliceOpStats(all))
	return all
}*/

func GetOpStatsByInterval(interval int64) []*OpStats {
	var all = make([]*OpStats, 0, 128)
	cmdstats.RLock()
	for _, s := range cmdstats.opmap {
		all = append(all, s.GetOpStatsByInterval(interval))
	}
	cmdstats.RUnlock()
	sort.Sort(sliceOpStats(all))
	return all
}

func ResetStats() {
	//由于session已经获取到了cmdstats.opmap中的结构体，所以这里不能重新分配只能置零
	//因此reset后命令数量不会减少
	cmdstats.RLock()
	for _, v := range cmdstats.opmap{
		v.totalCalls.Set(0)
		v.totalNsecs.Set(0)
		v.totalFails.Set(0)
		v.redis.errors.Set(0)
	}
	cmdstats.RUnlock()

	cmdstats.total.Set(0)
	cmdstats.fails.Set(0)
	cmdstats.redis.errors.Set(0)
	sessions.total.Set(sessions.alive.Int64())
}

func incrOpTotal() {
	cmdstats.total.Incr()
}

func incrOpRedisErrors() {
	cmdstats.redis.errors.Incr()
}

func incrOpFails(r *Request, err error) {
	if r != nil {
		var s *opStats
		s = getOpStats(r.OpStr, true)
		s.totalFails.Incr()
		s = getOpStats("ALL", true)
		s.totalFails.Incr()
	} 

	cmdstats.fails.Incr()
}

func incrOpStats(r *Request, t redis.RespType) {
	if r != nil {
		var s *opStats
		responseTime := time.Now().UnixNano() - r.ReceiveTime

		s = getOpStats(r.OpStr, true)
		s.incrOpStats(responseTime, t)
		s = getOpStats("ALL", true)
		s.incrOpStats(responseTime, t)

		switch t {
			case redis.TypeError:
				cmdstats.redis.errors.Incr()
		}
	}
}

var sessions struct {
	total atomic2.Int64
	alive atomic2.Int64
}

func incrSessions() int64 {
	sessions.total.Incr()
	return sessions.alive.Incr()
}

func decrSessions() {
	sessions.alive.Decr()
}

func SessionsTotal() int64 {
	return sessions.total.Int64()
}

func SessionsAlive() int64 {
	return sessions.alive.Int64()
}

type SysUsage struct {
	Now time.Time
	CPU float64
	*utils.Usage
}

var lastSysUsage atomic.Value

func init() {
	go func() {
		for {
			cpu, usage, err := utils.CPUUsage(time.Second)
			if err != nil {
				lastSysUsage.Store(&SysUsage{
					Now: time.Now(),
				})
			} else {
				lastSysUsage.Store(&SysUsage{
					Now: time.Now(),
					CPU: cpu, Usage: usage,
				})
			}
			if err != nil {
				time.Sleep(time.Second * 5)
			}
		}
	}()
}

func GetSysUsage() *SysUsage {
	if p := lastSysUsage.Load(); p != nil {
		return p.(*SysUsage)
	}
	return nil
}
