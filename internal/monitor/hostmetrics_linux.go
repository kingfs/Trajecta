//go:build linux

package monitor

import (
	"errors"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// hostClockTicksPerSecond is the Linux USER_HZ. The kernel reports CPU times in
// these ticks and Go exposes no portable constant for the value, but every
// architecture Linux supports on servers uses 100.
const hostClockTicksPerSecond = 100

// hostDiskFilesystems is the allow-list of local filesystems the page reports.
// Everything else on a modern machine is either a pseudo filesystem or a
// network mount, and neither answers the "will this box run out of disk"
// question the panel exists for.
var hostDiskFilesystems = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "f2fs": true, "overlay": true, "ntfs": true, "vfat": true,
	"exfat": true,
}

// hostDiskSkippedFilesystems names the pseudo filesystems to reject even though
// the allow-list already excludes them. They stay named so that a future entry
// added to the allow-list for a real filesystem cannot quietly admit one of
// them.
var hostDiskSkippedFilesystems = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "proc": true, "sysfs": true,
	"cgroup": true, "cgroup2": true, "autofs": true, "securityfs": true,
	"debugfs": true, "tracefs": true, "fusectl": true,
}

// hostDiskSkippedRoots are the kernel trees whose mounts are noise: they are
// never where application data lives and their sizes describe the kernel, not
// the disk.
var hostDiskSkippedRoots = []string{"/proc", "/sys", "/dev", "/run"}

// readSystemHostMetrics samples the Linux host and returns the response plus
// the raw counters the next call needs. The previous sample may be nil, which
// is the first call or a call after a long gap; in that case the delta-derived
// fields stay zero and this reading becomes the baseline.
func readSystemHostMetrics(now time.Time, prev *systemHostSample) (systemHostResponse, systemHostSample) {
	warnings := []string{}
	sample := systemHostSample{
		at:      now,
		ifaceRx: map[string]uint64{},
		ifaceTx: map[string]uint64{},
	}
	response := systemHostResponse{
		GeneratedAt: now.UTC(),
		Disk:        []systemHostDisk{},
		Network:     systemHostNetwork{Interfaces: []systemHostInterface{}},
		Warnings:    []string{},
	}

	// A negative elapsed interval can only come from a clock step; treating it
	// as no interval keeps the rate math finite.
	elapsedSeconds := 0.0
	if prev != nil {
		elapsedSeconds = now.Sub(prev.at).Seconds()
		if elapsedSeconds < 0 {
			elapsedSeconds = 0
		}
	}

	if hostname, err := os.Hostname(); err != nil {
		warnings = append(warnings, "hostname: "+err.Error())
	} else {
		response.Host.Hostname = hostname
	}

	uptimeSeconds := 0.0
	if seconds, err := readProcUptimeSeconds(); err != nil {
		warnings = append(warnings, "uptime: "+err.Error())
	} else {
		uptimeSeconds = seconds
		response.Host.UptimeSeconds = seconds
	}

	if kernel, err := readProcTrimmed("/proc/sys/kernel/osrelease"); err != nil {
		warnings = append(warnings, "kernel release: "+err.Error())
	} else {
		response.Host.Kernel = kernel
	}

	aggregateTicks, perCoreTicks, err := readProcStatCPU()
	if err != nil {
		warnings = append(warnings, "proc/stat: "+err.Error())
	}
	sample.cpuTicks = aggregateTicks
	sample.perCoreTicks = perCoreTicks
	cores := len(perCoreTicks)
	if cores == 0 {
		// Without per-core lines the page still needs a core count, and the
		// runtime's view of the machine is the best available answer.
		cores = runtime.NumCPU()
	}
	response.CPU.Cores = cores
	response.CPU.PerCorePercent = make([]float64, cores)
	if prev != nil {
		percent := cpuPercentages(prev.cpuTicks, aggregateTicks)
		response.CPU.UserPercent = percent.user
		response.CPU.SystemPercent = percent.system
		response.CPU.IowaitPercent = percent.iowait
		response.CPU.IdlePercent = percent.idle
		response.CPU.UsagePercent = busyPercent(percent.idle)
		for index := range perCoreTicks {
			if index >= cores || index >= len(prev.perCoreTicks) {
				break
			}
			core := cpuPercentages(prev.perCoreTicks[index], perCoreTicks[index])
			response.CPU.PerCorePercent[index] = busyPercent(core.idle)
		}
	}

	if load, err := readProcLoadAvg(); err != nil {
		warnings = append(warnings, "loadavg: "+err.Error())
	} else {
		response.CPU.Load1, response.CPU.Load5, response.CPU.Load15 = load[0], load[1], load[2]
	}

	response.Memory, warnings = readHostMemory(warnings)

	disks, diskWarnings := readHostDisks()
	response.Disk = disks
	warnings = append(warnings, diskWarnings...)

	interfaces, err := readProcNetDev()
	if err != nil {
		warnings = append(warnings, "net/dev: "+err.Error())
	} else {
		for _, iface := range interfaces {
			sample.ifaceRx[iface.name] = iface.rx
			sample.ifaceTx[iface.name] = iface.tx
			entry := systemHostInterface{
				Name:     iface.name,
				Loopback: iface.name == "lo",
				RxBytes:  iface.rx,
				TxBytes:  iface.tx,
			}
			if prev != nil && elapsedSeconds > 0 {
				// An interface that appeared since the previous sample has no
				// baseline; its lifetime counters are shown and only its rate
				// starts at zero.
				if before, ok := prev.ifaceRx[iface.name]; ok {
					entry.RxBytesPerSec = float64(tickDelta(iface.rx, before)) / elapsedSeconds
				}
				if before, ok := prev.ifaceTx[iface.name]; ok {
					entry.TxBytesPerSec = float64(tickDelta(iface.tx, before)) / elapsedSeconds
				}
			}
			response.Network.Interfaces = append(response.Network.Interfaces, entry)
			if iface.name != "lo" {
				sample.rxBytes += iface.rx
				sample.txBytes += iface.tx
			}
		}
	}
	if prev != nil && elapsedSeconds > 0 {
		response.Network.RxBytesPerSec = float64(tickDelta(sample.rxBytes, prev.rxBytes)) / elapsedSeconds
		response.Network.TxBytesPerSec = float64(tickDelta(sample.txBytes, prev.txBytes)) / elapsedSeconds
	}

	process, processWarnings := readProgramProcess(now, uptimeSeconds, prev, elapsedSeconds, &sample)
	response.Process = process
	response.Process.OpenFDs = countOpenFileDescriptors()
	response.Process.PID = os.Getpid()
	warnings = append(warnings, processWarnings...)

	response.Warnings = warnings
	return response, sample
}

// cpuPercent is the four buckets the page renders for one CPU.
type cpuPercent struct {
	user   float64
	system float64
	iowait float64
	idle   float64
}

// cpuPercentages turns two CPU tick sets into percentages. The denominator is
// the sum of every column, so time spent in states the page does not name still
// counts against the total instead of inflating the named buckets.
func cpuPercentages(prev, current systemHostCPUTicks) cpuPercent {
	userTicks := tickDelta(current.user, prev.user) + tickDelta(current.nice, prev.nice)
	systemTicks := tickDelta(current.system, prev.system) +
		tickDelta(current.irq, prev.irq) + tickDelta(current.softirq, prev.softirq)
	iowaitTicks := tickDelta(current.iowait, prev.iowait)
	idleTicks := tickDelta(current.idle, prev.idle)
	total := userTicks + systemTicks + iowaitTicks + idleTicks + tickDelta(current.steal, prev.steal)
	if total == 0 {
		return cpuPercent{}
	}
	asPercent := func(ticks uint64) float64 {
		return float64(ticks) * 100 / float64(total)
	}
	return cpuPercent{
		user:   asPercent(userTicks),
		system: asPercent(systemTicks),
		iowait: asPercent(iowaitTicks),
		idle:   asPercent(idleTicks),
	}
}

// busyPercent converts an idle share into a usage share. Iowait counts as busy
// here because a disk-bound process is still waiting on the machine.
func busyPercent(idle float64) float64 {
	busy := 100 - idle
	if busy < 0 {
		return 0
	}
	if busy > 100 {
		return 100
	}
	return busy
}

// readHostMemory parses /proc/meminfo. A missing or empty MemTotal is reported
// as a warning rather than as a machine with no memory.
func readHostMemory(warnings []string) (systemHostMemory, []string) {
	values, err := readProcMeminfo()
	if err != nil {
		return systemHostMemory{}, append(warnings, "meminfo: "+err.Error())
	}
	total := values["MemTotal"]
	if total == 0 {
		return systemHostMemory{}, append(warnings, "meminfo: MemTotal is missing")
	}
	available, ok := values["MemAvailable"]
	if !ok {
		// Kernels before 3.14 do not publish MemAvailable; the traditional
		// approximation is free memory plus everything reclaimable.
		available = values["MemFree"] + values["Buffers"] + values["Cached"] + values["SReclaimable"]
	}
	if available > total {
		available = total
	}
	memory := systemHostMemory{
		TotalBytes:     total,
		AvailableBytes: available,
		UsedBytes:      total - available,
		CachedBytes:    values["Cached"] + values["SReclaimable"],
	}
	memory.UsedPercent = float64(memory.UsedBytes) * 100 / float64(total)

	if swapTotal := values["SwapTotal"]; swapTotal > 0 {
		swapFree := values["SwapFree"]
		if swapFree > swapTotal {
			swapFree = swapTotal
		}
		memory.SwapTotalBytes = swapTotal
		memory.SwapUsedBytes = swapTotal - swapFree
		memory.SwapUsedPct = float64(memory.SwapUsedBytes) * 100 / float64(swapTotal)
	}
	return memory, warnings
}

// readHostDisks walks /proc/mounts, keeps the local filesystems worth watching
// and sorts them largest first, so the cap keeps the mounts that can actually
// fill up. Mounts that fail statfs are reported as warnings and skipped: one
// unreadable mount must not blank the panel.
func readHostDisks() ([]systemHostDisk, []string) {
	lines, err := readProcLines("/proc/mounts")
	if err != nil {
		return []systemHostDisk{}, []string{"mounts: " + err.Error()}
	}
	warnings := []string{}
	seen := map[string]bool{}
	disks := []systemHostDisk{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		device := unescapeMountField(fields[0])
		mount := unescapeMountField(fields[1])
		filesystem := fields[2]
		if !hostDiskFilesystems[filesystem] || hostDiskSkippedFilesystems[filesystem] {
			continue
		}
		if hostDiskSkippedMount(mount) {
			continue
		}
		key := device + "\x00" + mount
		if seen[key] {
			continue
		}
		seen[key] = true

		var stat syscall.Statfs_t
		if err := syscall.Statfs(mount, &stat); err != nil {
			warnings = append(warnings, "statfs "+mount+": "+err.Error())
			continue
		}
		if stat.Bsize <= 0 {
			warnings = append(warnings, "statfs "+mount+": unusable block size")
			continue
		}
		blockSize := uint64(stat.Bsize)
		blocks := uint64(stat.Blocks)
		availableBlocks := uint64(stat.Bavail)
		usedBlocks := uint64(0)
		if blocks > availableBlocks {
			usedBlocks = blocks - availableBlocks
		}
		disk := systemHostDisk{
			Mount:          mount,
			Filesystem:     filesystem,
			Device:         device,
			TotalBytes:     blocks * blockSize,
			UsedBytes:      usedBlocks * blockSize,
			AvailableBytes: availableBlocks * blockSize,
		}
		// The denominator excludes reserved blocks for the same reason the
		// numerator does, which is what makes the percentage add up to what
		// root can actually use.
		if used := disk.UsedBytes + disk.AvailableBytes; used > 0 {
			disk.UsedPercent = float64(disk.UsedBytes) * 100 / float64(used)
		}
		disks = append(disks, disk)
	}
	sort.SliceStable(disks, func(i, j int) bool { return disks[i].TotalBytes > disks[j].TotalBytes })
	if len(disks) > systemHostDiskLimit {
		disks = disks[:systemHostDiskLimit]
	}
	return disks, warnings
}

// hostDiskSkippedMount reports whether a mount lives inside a kernel tree the
// panel ignores.
func hostDiskSkippedMount(mount string) bool {
	for _, root := range hostDiskSkippedRoots {
		if mount == root || strings.HasPrefix(mount, root+"/") {
			return true
		}
	}
	return false
}

// unescapeMountField decodes the octal escapes /proc/mounts uses for space,
// tab, newline and backslash, so a mount point containing a space is reported
// as the path an operator would type.
func unescapeMountField(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+3 < len(value) {
			if parsed, err := strconv.ParseUint(value[index+1:index+4], 8, 8); err == nil {
				out.WriteByte(byte(parsed))
				index += 3
				continue
			}
		}
		out.WriteByte(value[index])
	}
	return out.String()
}

// readProgramProcess fills the process panel from /proc/self. CPU time is a
// delta over the same interval as the host rates; the tick counters are stored
// in the sample so the next call can subtract them.
func readProgramProcess(now time.Time, uptimeSeconds float64, prev *systemHostSample, elapsedSeconds float64, sample *systemHostSample) (systemHostProcess, []string) {
	process := systemHostProcess{}
	warnings := []string{}
	pageSize := uint64(os.Getpagesize())
	if statm, err := readProcStatm(); err != nil {
		warnings = append(warnings, "statm: "+err.Error())
	} else {
		process.VSZBytes = statm.virtual * pageSize
		process.RSSBytes = statm.resident * pageSize
	}
	stat, err := readProcSelfStat()
	if err != nil {
		warnings = append(warnings, "self/stat: "+err.Error())
		return process, warnings
	}
	process.Threads = stat.threads
	sample.processTicks = stat.utime + stat.stime
	if uptimeSeconds > 0 && stat.startTicks > 0 {
		boot := now.Add(-time.Duration(uptimeSeconds * float64(time.Second)))
		started := float64(stat.startTicks) / hostClockTicksPerSecond
		process.StartedAt = boot.Add(time.Duration(started * float64(time.Second))).UTC()
	}
	if prev != nil && elapsedSeconds > 0 {
		cpuTicks := tickDelta(sample.processTicks, prev.processTicks)
		process.CPUPercent = float64(cpuTicks) / hostClockTicksPerSecond / elapsedSeconds * 100
	}
	return process, warnings
}

// countOpenFileDescriptors counts the entries in /proc/self/fd. A short read is
// still useful, so the readdir error is ignored; entries can disappear between
// the listing and the count, which is why the count is approximate by nature.
func countOpenFileDescriptors() int {
	entries, _ := os.ReadDir("/proc/self/fd")
	return len(entries)
}

// hostProcessMemory is statm's virtual and resident sizes, in pages.
type hostProcessMemory struct {
	virtual  uint64
	resident uint64
}

// readProcStatm reads the first two /proc/self/statm fields.
func readProcStatm() (hostProcessMemory, error) {
	lines, err := readProcLines("/proc/self/statm")
	if err != nil {
		return hostProcessMemory{}, err
	}
	if len(lines) == 0 {
		return hostProcessMemory{}, errors.New("empty file")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 2 {
		return hostProcessMemory{}, errors.New("short statm line")
	}
	virtual, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return hostProcessMemory{}, err
	}
	resident, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return hostProcessMemory{}, err
	}
	return hostProcessMemory{virtual: virtual, resident: resident}, nil
}

// hostProcessStat is the slice of /proc/self/stat the panel needs: the thread
// count, the CPU time in clock ticks, and the start time since boot.
type hostProcessStat struct {
	threads    int
	utime      uint64
	stime      uint64
	startTicks uint64
}

// readProcSelfStat reads /proc/self/stat. The comm field can contain spaces and
// parentheses, so the remaining fields are located from the last ')' rather
// than by splitting the whole line.
func readProcSelfStat() (hostProcessStat, error) {
	lines, err := readProcLines("/proc/self/stat")
	if err != nil {
		return hostProcessStat{}, err
	}
	if len(lines) == 0 {
		return hostProcessStat{}, errors.New("empty file")
	}
	line := lines[0]
	end := strings.LastIndexByte(line, ')')
	if end < 0 || end+1 >= len(line) {
		return hostProcessStat{}, errors.New("malformed stat line")
	}
	fields := strings.Fields(line[end+1:])
	if len(fields) < 20 {
		return hostProcessStat{}, errors.New("short stat line")
	}
	// fields[0] is field 3 (state), so field N lives at fields[N-3].
	return hostProcessStat{
		utime:      parseStatUint(fields[11]),      // field 14
		stime:      parseStatUint(fields[12]),      // field 15
		threads:    int(parseStatUint(fields[17])), // field 20
		startTicks: parseStatUint(fields[19]),      // field 22
	}, nil
}

// parseStatUint parses one numeric stat field. A field that does not parse is
// reported as zero: losing one counter is better than losing the panel.
func parseStatUint(field string) uint64 {
	value, err := strconv.ParseUint(field, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// readProcStatCPU reads the aggregate "cpu" line and every "cpuN" line of
// /proc/stat.
func readProcStatCPU() (systemHostCPUTicks, []systemHostCPUTicks, error) {
	lines, err := readProcLines("/proc/stat")
	if err != nil {
		return systemHostCPUTicks{}, nil, err
	}
	var aggregate systemHostCPUTicks
	perCore := []systemHostCPUTicks{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		ticks, ok := parseCPUTicks(fields[1:])
		if !ok {
			continue
		}
		if fields[0] == "cpu" {
			aggregate = ticks
			continue
		}
		perCore = append(perCore, ticks)
	}
	if aggregate.total() == 0 && len(perCore) == 0 {
		return systemHostCPUTicks{}, nil, errors.New("no cpu lines")
	}
	return aggregate, perCore, nil
}

// parseCPUTicks reads the column values of one /proc/stat CPU line. Missing
// trailing columns are zero, which is how older kernels omit steal time.
func parseCPUTicks(fields []string) (systemHostCPUTicks, bool) {
	if len(fields) < 4 {
		return systemHostCPUTicks{}, false
	}
	value := func(index int) uint64 {
		if index >= len(fields) {
			return 0
		}
		parsed, err := strconv.ParseUint(fields[index], 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	}
	return systemHostCPUTicks{
		user:    value(0),
		nice:    value(1),
		system:  value(2),
		idle:    value(3),
		iowait:  value(4),
		irq:     value(5),
		softirq: value(6),
		steal:   value(7),
	}, true
}

// readProcUptimeSeconds reads the kernel uptime. It is also the boot time
// anchor for the process start timestamp.
func readProcUptimeSeconds() (float64, error) {
	lines, err := readProcLines("/proc/uptime")
	if err != nil {
		return 0, err
	}
	if len(lines) == 0 {
		return 0, errors.New("empty file")
	}
	fields := strings.Fields(lines[0])
	if len(fields) == 0 {
		return 0, errors.New("empty line")
	}
	return strconv.ParseFloat(fields[0], 64)
}

// readProcLoadAvg reads the three load averages.
func readProcLoadAvg() ([3]float64, error) {
	var load [3]float64
	lines, err := readProcLines("/proc/loadavg")
	if err != nil {
		return load, err
	}
	if len(lines) == 0 {
		return load, errors.New("empty file")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 3 {
		return load, errors.New("short loadavg line")
	}
	for index := 0; index < 3; index++ {
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil {
			return load, err
		}
		load[index] = value
	}
	return load, nil
}

// readProcMeminfo reads /proc/meminfo into bytes. Every current field is
// reported in kibibytes, and the unit token is checked so a future field in
// another unit cannot be misread by a factor of 1024.
func readProcMeminfo() (map[string]uint64, error) {
	lines, err := readProcLines("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	values := map[string]uint64{}
	for _, line := range lines {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		amount, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
			amount *= 1024
		}
		values[strings.TrimSpace(key)] = amount
	}
	if len(values) == 0 {
		return nil, errors.New("no meminfo fields")
	}
	return values, nil
}

// hostNetworkCounters is one interface's lifetime byte counts.
type hostNetworkCounters struct {
	name string
	rx   uint64
	tx   uint64
}

// readProcNetDev reads the per-interface byte counters from /proc/net/dev. The
// file has two header lines, which are skipped by requiring the colon that only
// interface lines carry.
func readProcNetDev() ([]hostNetworkCounters, error) {
	lines, err := readProcLines("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	counters := []hostNetworkCounters{}
	for _, line := range lines {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		// Column 0 is received bytes and column 8 is transmitted bytes.
		if len(fields) < 9 {
			continue
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			continue
		}
		counters = append(counters, hostNetworkCounters{name: strings.TrimSpace(name), rx: rx, tx: tx})
	}
	if len(counters) == 0 {
		return nil, errors.New("no interface counters")
	}
	return counters, nil
}

// readProcTrimmed reads a one-value /proc file, such as the kernel release.
func readProcTrimmed(path string) (string, error) {
	lines, err := readProcLines(path)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", errors.New("empty file")
	}
	return strings.TrimSpace(lines[0]), nil
}

// readProcLines reads a small /proc file into its lines. These files are tiny
// and change on every sample, so a full re-read is cheaper to reason about than
// keeping a handle open across requests.
func readProcLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}
