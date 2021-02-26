// +build windows

package collector

/*
#cgo LDFLAGS: -lDXGI
#include <windows.h>
#include <dxgi.h>

#include <initguid.h>
DEFINE_GUID(GUID_IDXGI_FACTORY, 0x7b7166ec, 0x21c7, 0x44ae, 0xb2, 0x1a, 0xc9, 0xae, 0x32, 0x1a, 0xe3, 0x69);

struct DxgiAdapterDescription
{
	wchar_t description[128];
	LUID 	luid;
};

UINT GetDxgiAdapterCount()
{
	UINT dxgi_adapter_count = 0;
	IDXGIFactory *dxgi_factory = NULL;
	if (CreateDXGIFactory(&GUID_IDXGI_FACTORY, (void**)&dxgi_factory) == S_OK && dxgi_factory != NULL)
	{
		IDXGIAdapter *dxgi_adapter = NULL;
		while ((*dxgi_factory->lpVtbl->EnumAdapters)(dxgi_factory, dxgi_adapter_count, &dxgi_adapter) == S_OK)
		{
			dxgi_adapter_count++;
			if (dxgi_adapter != NULL)
			{
				(*dxgi_adapter->lpVtbl->Release)(dxgi_adapter);
			}
		}
		(*dxgi_factory->lpVtbl->Release)(dxgi_factory);
	}
	return dxgi_adapter_count;
}

UINT GetDxgiAdapterDescriptions(struct DxgiAdapterDescription *dxgi_adapter_descriptions, UINT dxgi_adapter_description_count)
{
	IDXGIFactory *dxgi_factory = NULL;
	struct DxgiAdapterDescription *current_dxgi_adapter_description = dxgi_adapter_descriptions;
	if (CreateDXGIFactory(&GUID_IDXGI_FACTORY, (void**)&dxgi_factory) == S_OK && dxgi_factory != NULL)
	{
		UINT dxgi_adapter_index = 0;
		IDXGIAdapter *dxgi_adapter = NULL;
		while (dxgi_adapter_description_count && (*dxgi_factory->lpVtbl->EnumAdapters)(dxgi_factory, dxgi_adapter_index, &dxgi_adapter) == S_OK)
		{
			dxgi_adapter_index++;
			if (dxgi_adapter != NULL)
			{
				DXGI_ADAPTER_DESC dxgi_adapter_description;
				if ((*dxgi_adapter->lpVtbl->GetDesc)(dxgi_adapter, &dxgi_adapter_description) == S_OK)
				{
					memcpy(current_dxgi_adapter_description->description, dxgi_adapter_description.Description, sizeof(current_dxgi_adapter_description->description));
					current_dxgi_adapter_description->luid = dxgi_adapter_description.AdapterLuid;
					++current_dxgi_adapter_description;
					--dxgi_adapter_description_count;
				}
				(*dxgi_adapter->lpVtbl->Release)(dxgi_adapter);
			}
		}
		(*dxgi_factory->lpVtbl->Release)(dxgi_factory);
		return dxgi_adapter_index;
	}
	return current_dxgi_adapter_description - dxgi_adapter_descriptions;
}
*/
import "C"
import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/StackExchange/wmi"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"gopkg.in/alecthomas/kingpin.v2"
)

func init() {
	registerCollector("process", newProcessCollector, "Process", "GPU Process Memory", "GPU Engine")
}

var (
	processWhitelist = kingpin.Flag(
		"collector.process.whitelist",
		"Regexp of processes to include. Process name must both match whitelist and not match blacklist to be included.",
	).Default(".*").String()
	processBlacklist = kingpin.Flag(
		"collector.process.blacklist",
		"Regexp of processes to exclude. Process name must both match whitelist and not match blacklist to be included.",
	).Default("").String()
)

type processCollector struct {
	StartTime         *prometheus.Desc
	CPUTimeTotal      *prometheus.Desc
	HandleCount       *prometheus.Desc
	IOBytesTotal      *prometheus.Desc
	IOOperationsTotal *prometheus.Desc
	PageFaultsTotal   *prometheus.Desc
	PageFileBytes     *prometheus.Desc
	PoolBytes         *prometheus.Desc
	PriorityBase      *prometheus.Desc
	PrivateBytes      *prometheus.Desc
	ThreadCount       *prometheus.Desc
	VirtualBytes      *prometheus.Desc
	WorkingSet        *prometheus.Desc

	processWhitelistPattern *regexp.Regexp
	processBlacklistPattern *regexp.Regexp

	dxgiAdapterLuidDescriptionMap map[string]string
}

// https://docs.microsoft.com/en-us/windows/win32/api/dxgi/ns-dxgi-dxgi_adapter_desc
type dxgiAdapterDescription struct {
	description  [128]C.wchar_t
	luidLowPart  C.DWORD
	luidHighPart C.LONG
}

// NewProcessCollector ...
func newProcessCollector() (Collector, error) {
	const subsystem = "process"

	dxgiAdapterCount := C.GetDxgiAdapterCount()
	var dxgiAdapterDescriptions []C.struct_DxgiAdapterDescription
	dxgiAdapterLuidDescriptionMap := make(map[string]string)
	if dxgiAdapterCount > 0 {
		dxgiAdapterDescriptions = make([]C.struct_DxgiAdapterDescription, dxgiAdapterCount)
		dxgiAdapterDescriptionCount := C.GetDxgiAdapterDescriptions(&dxgiAdapterDescriptions[0], dxgiAdapterCount)
		for dxgiAdapterDescriptionIndex := C.UINT(0); dxgiAdapterDescriptionIndex < dxgiAdapterDescriptionCount; dxgiAdapterDescriptionIndex++ {
			description := syscall.UTF16ToString((*[128]uint16)(unsafe.Pointer(&dxgiAdapterDescriptions[dxgiAdapterDescriptionIndex].description))[:])
			luid := fmt.Sprintf("0x%08X_0x%08X", dxgiAdapterDescriptions[dxgiAdapterDescriptionIndex].luid.HighPart, dxgiAdapterDescriptions[dxgiAdapterDescriptionIndex].luid.LowPart)
			dxgiAdapterLuidDescriptionMap[luid] = description
		}
	}

	if *processWhitelist == ".*" && *processBlacklist == "" {
		log.Warn("No filters specified for process collector. This will generate a very large number of metrics!")
	}

	return &processCollector{
		StartTime: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "start_time"),
			"Time of process start.",
			[]string{"process", "process_id", "creating_process_id"},
			nil,
		),
		CPUTimeTotal: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "cpu_time_total"),
			"Returns elapsed time that all of the threads of this process used the processor to execute instructions by mode (privileged, user). An instruction is the basic unit of execution in a computer, a thread is the object that executes instructions, and a process is the object created when a program is run. Code executed to handle some hardware interrupts and trap conditions is included in this count.",
			[]string{"process", "process_id", "creating_process_id", "mode"},
			nil,
		),
		HandleCount: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "handle_count"),
			"Total number of handles the process has open. This number is the sum of the handles currently open by each thread in the process.",
			[]string{"process", "process_id", "creating_process_id"},
			nil,
		),
		IOBytesTotal: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "io_bytes_total"),
			"Bytes issued to I/O operations in different modes (read, write, other). This property counts all I/O activity generated by the process to include file, network, and device I/Os. Read and write mode includes data operations; other mode includes those that do not involve data, such as control operations. ",
			[]string{"process", "process_id", "creating_process_id", "mode"},
			nil,
		),
		IOOperationsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "io_operations_total"),
			"I/O operations issued in different modes (read, write, other). This property counts all I/O activity generated by the process to include file, network, and device I/Os. Read and write mode includes data operations; other mode includes those that do not involve data, such as control operations. ",
			[]string{"process", "process_id", "creating_process_id", "mode"},
			nil,
		),
		PageFaultsTotal: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "page_faults_total"),
			"Page faults by the threads executing in this process. A page fault occurs when a thread refers to a virtual memory page that is not in its working set in main memory. This can cause the page not to be fetched from disk if it is on the standby list and hence already in main memory, or if it is in use by another process with which the page is shared.",
			[]string{"process", "process_id", "creating_process_id"},
			nil,
		),
		PageFileBytes: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "page_file_bytes"),
			"Current number of bytes this process has used in the paging file(s). Paging files are used to store pages of memory used by the process that are not contained in other files. Paging files are shared by all processes, and lack of space in paging files can prevent other processes from allocating memory.",
			[]string{"process", "process_id", "creating_process_id"},
			nil,
		),
		PoolBytes: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "pool_bytes"),
			"Pool Bytes is the last observed number of bytes in the paged or nonpaged pool. The nonpaged pool is an area of system memory (physical memory used by the operating system) for objects that cannot be written to disk, but must remain in physical memory as long as they are allocated. The paged pool is an area of system memory (physical memory used by the operating system) for objects that can be written to disk when they are not being used. Nonpaged pool bytes is calculated differently than paged pool bytes, so it might not equal the total of paged pool bytes.",
			[]string{"process", "process_id", "creating_process_id", "pool"},
			nil,
		),
		PriorityBase: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "priority_base"),
			"Current base priority of this process. Threads within a process can raise and lower their own base priority relative to the process base priority of the process.",
			[]string{"process", "process_id", "creating_process_id"},
			nil,
		),
		PrivateBytes: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "private_bytes"),
			"Current number of bytes this process has allocated that cannot be shared with other processes.",
			[]string{"process", "process_id", "creating_process_id"},
			nil,
		),
		ThreadCount: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "thread_count"),
			"Number of threads currently active in this process. An instruction is the basic unit of execution in a processor, and a thread is the object that executes instructions. Every running process has at least one thread.",
			[]string{"process", "process_id", "creating_process_id"},
			nil,
		),
		VirtualBytes: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "virtual_bytes"),
			"Current size, in bytes, of the virtual address space that the process is using. Use of virtual address space does not necessarily imply corresponding use of either disk or main memory pages. Virtual space is finite and, by using too much, the process can limit its ability to load libraries.",
			[]string{"process", "process_id", "creating_process_id"},
			nil,
		),
		WorkingSet: prometheus.NewDesc(
			prometheus.BuildFQName(Namespace, subsystem, "working_set"),
			"Maximum number of bytes in the working set of this process at any point in time. The working set is the set of memory pages touched recently by the threads in the process. If free memory in the computer is above a threshold, pages are left in the working set of a process even if they are not in use. When free memory falls below a threshold, pages are trimmed from working sets. If they are needed, they are then soft-faulted back into the working set before they leave main memory.",
			[]string{"process", "process_id", "creating_process_id"},
			nil,
		),
		processWhitelistPattern: regexp.MustCompile(fmt.Sprintf("^(?:%s)$", *processWhitelist)),
		processBlacklistPattern: regexp.MustCompile(fmt.Sprintf("^(?:%s)$", *processBlacklist)),

		dxgiAdapterLuidDescriptionMap: dxgiAdapterLuidDescriptionMap,
	}, nil
}

type perflibProcess struct {
	Name                    string
	PercentProcessorTime    float64 `perflib:"% Processor Time"`
	PercentPrivilegedTime   float64 `perflib:"% Privileged Time"`
	PercentUserTime         float64 `perflib:"% User Time"`
	CreatingProcessID       float64 `perflib:"Creating Process ID"`
	ElapsedTime             float64 `perflib:"Elapsed Time"`
	HandleCount             float64 `perflib:"Handle Count"`
	IDProcess               float64 `perflib:"ID Process"`
	IODataBytesPerSec       float64 `perflib:"IO Data Bytes/sec"`
	IODataOperationsPerSec  float64 `perflib:"IO Data Operations/sec"`
	IOOtherBytesPerSec      float64 `perflib:"IO Other Bytes/sec"`
	IOOtherOperationsPerSec float64 `perflib:"IO Other Operations/sec"`
	IOReadBytesPerSec       float64 `perflib:"IO Read Bytes/sec"`
	IOReadOperationsPerSec  float64 `perflib:"IO Read Operations/sec"`
	IOWriteBytesPerSec      float64 `perflib:"IO Write Bytes/sec"`
	IOWriteOperationsPerSec float64 `perflib:"IO Write Operations/sec"`
	PageFaultsPerSec        float64 `perflib:"Page Faults/sec"`
	PageFileBytesPeak       float64 `perflib:"Page File Bytes Peak"`
	PageFileBytes           float64 `perflib:"Page File Bytes"`
	PoolNonpagedBytes       float64 `perflib:"Pool Nonpaged Bytes"`
	PoolPagedBytes          float64 `perflib:"Pool Paged Bytes"`
	PriorityBase            float64 `perflib:"Priority Base"`
	PrivateBytes            float64 `perflib:"Private Bytes"`
	ThreadCount             float64 `perflib:"Thread Count"`
	VirtualBytesPeak        float64 `perflib:"Virtual Bytes Peak"`
	VirtualBytes            float64 `perflib:"Virtual Bytes"`
	WorkingSetPrivate       float64 `perflib:"Working Set - Private"`
	WorkingSetPeak          float64 `perflib:"Working Set Peak"`
	WorkingSet              float64 `perflib:"Working Set"`
}

type perflibGpuProcessMemory struct {
	Name           string
	DedicatedUsage float64 `perflib:"Dedicated Usage"`
	SharedUsage    float64 `perflib:"Shared Usage"`
}

type perflibGpuEngine struct {
	Name                  string
	UtilizationPercentage float64 `perflib:"Utilization Percentage"`
}

type ProcessGpuMetrics struct {
	DedicatedUsage        map[string]float64
	SharedUsage           map[string]float64
	UtilizationPercentage map[string]float64
}

type WorkerProcess struct {
	AppPoolName string
	ProcessId   uint64
}

var pidLuidRegexp = regexp.MustCompile("pid_([0-9]+)_luid_(0x[0-9a-zA-Z]{8}_0x[0-9a-zA-Z]{8})")

func extractPidAndLuid(name string) (string, string) {
	match := pidLuidRegexp.FindStringSubmatch(name)
	return match[1], match[2]
}

func (c *processCollector) Collect(ctx *ScrapeContext, ch chan<- prometheus.Metric) error {
	data := make([]perflibProcess, 0)
	err := unmarshalObject(ctx.perfObjects["Process"], &data)
	if err != nil {
		return err
	}

	gpuProcessMemory := make([]perflibGpuProcessMemory, 0)
	err = unmarshalObject(ctx.perfObjects["GPU Process Memory"], &gpuProcessMemory)
	if err != nil {
		return err
	}

	gpuEngine := make([]perflibGpuEngine, 0)
	err = unmarshalObject(ctx.perfObjects["GPU Engine"], &gpuEngine)
	processGpuMetrics := make(map[string]*ProcessGpuMetrics)
	if err != nil {
		return err
	}

	for _, gpuProcess := range gpuProcessMemory {
		pid, luid := extractPidAndLuid(gpuProcess.Name)
		_, dxgiAdapterPresent := c.dxgiAdapterLuidDescriptionMap[luid]
		if dxgiAdapterPresent {
			_, pidPresent := processGpuMetrics[pid]
			if pidPresent == false {
				processGpuMetrics[pid] = &ProcessGpuMetrics{}
			}
		}

	}
	for _, gpuProcess := range gpuEngine {
		pid, luid := extractPidAndLuid(gpuProcess.Name)
		_, dxgiAdapterPresent := c.dxgiAdapterLuidDescriptionMap[luid]
		if dxgiAdapterPresent {
			_, pidPresent := processGpuMetrics[pid]
			if pidPresent == false {
				processGpuMetrics[pid] = &ProcessGpuMetrics{}
			}
		}
	}

	var dst_wp []WorkerProcess
	q_wp := queryAll(&dst_wp)
	if err := wmi.QueryNamespace(q_wp, &dst_wp, "root\\WebAdministration"); err != nil {
		log.Debugf("Could not query WebAdministration namespace for IIS worker processes: %v. Skipping", err)
	}

	for _, process := range data {
		if process.Name == "_Total" ||
			c.processBlacklistPattern.MatchString(process.Name) ||
			!c.processWhitelistPattern.MatchString(process.Name) {
			continue
		}
		// Duplicate processes are suffixed # and an index number. Remove those.
		processName := strings.Split(process.Name, "#")[0]
		pid := strconv.FormatUint(uint64(process.IDProcess), 10)
		cpid := strconv.FormatUint(uint64(process.CreatingProcessID), 10)

		for _, wp := range dst_wp {
			if wp.ProcessId == uint64(process.IDProcess) {
				processName = strings.Join([]string{processName, wp.AppPoolName}, "_")
				break
			}
		}

		ch <- prometheus.MustNewConstMetric(
			c.StartTime,
			prometheus.GaugeValue,
			process.ElapsedTime,
			processName,
			pid,
			cpid,
		)

		ch <- prometheus.MustNewConstMetric(
			c.HandleCount,
			prometheus.GaugeValue,
			process.HandleCount,
			processName,
			pid,
			cpid,
		)

		ch <- prometheus.MustNewConstMetric(
			c.CPUTimeTotal,
			prometheus.CounterValue,
			process.PercentPrivilegedTime,
			processName,
			pid,
			cpid,
			"privileged",
		)

		ch <- prometheus.MustNewConstMetric(
			c.CPUTimeTotal,
			prometheus.CounterValue,
			process.PercentUserTime,
			processName,
			pid,
			cpid,
			"user",
		)

		ch <- prometheus.MustNewConstMetric(
			c.IOBytesTotal,
			prometheus.CounterValue,
			process.IOOtherBytesPerSec,
			processName,
			pid,
			cpid,
			"other",
		)

		ch <- prometheus.MustNewConstMetric(
			c.IOOperationsTotal,
			prometheus.CounterValue,
			process.IOOtherOperationsPerSec,
			processName,
			pid,
			cpid,
			"other",
		)

		ch <- prometheus.MustNewConstMetric(
			c.IOBytesTotal,
			prometheus.CounterValue,
			process.IOReadBytesPerSec,
			processName,
			pid,
			cpid,
			"read",
		)

		ch <- prometheus.MustNewConstMetric(
			c.IOOperationsTotal,
			prometheus.CounterValue,
			process.IOReadOperationsPerSec,
			processName,
			pid,
			cpid,
			"read",
		)

		ch <- prometheus.MustNewConstMetric(
			c.IOBytesTotal,
			prometheus.CounterValue,
			process.IOWriteBytesPerSec,
			processName,
			pid,
			cpid,
			"write",
		)

		ch <- prometheus.MustNewConstMetric(
			c.IOOperationsTotal,
			prometheus.CounterValue,
			process.IOWriteOperationsPerSec,
			processName,
			pid,
			cpid,
			"write",
		)

		ch <- prometheus.MustNewConstMetric(
			c.PageFaultsTotal,
			prometheus.CounterValue,
			process.PageFaultsPerSec,
			processName,
			pid,
			cpid,
		)

		ch <- prometheus.MustNewConstMetric(
			c.PageFileBytes,
			prometheus.GaugeValue,
			process.PageFileBytes,
			processName,
			pid,
			cpid,
		)

		ch <- prometheus.MustNewConstMetric(
			c.PoolBytes,
			prometheus.GaugeValue,
			process.PoolNonpagedBytes,
			processName,
			pid,
			cpid,
			"nonpaged",
		)

		ch <- prometheus.MustNewConstMetric(
			c.PoolBytes,
			prometheus.GaugeValue,
			process.PoolPagedBytes,
			processName,
			pid,
			cpid,
			"paged",
		)

		ch <- prometheus.MustNewConstMetric(
			c.PriorityBase,
			prometheus.GaugeValue,
			process.PriorityBase,
			processName,
			pid,
			cpid,
		)

		ch <- prometheus.MustNewConstMetric(
			c.PrivateBytes,
			prometheus.GaugeValue,
			process.PrivateBytes,
			processName,
			pid,
			cpid,
		)

		ch <- prometheus.MustNewConstMetric(
			c.ThreadCount,
			prometheus.GaugeValue,
			process.ThreadCount,
			processName,
			pid,
			cpid,
		)

		ch <- prometheus.MustNewConstMetric(
			c.VirtualBytes,
			prometheus.GaugeValue,
			process.VirtualBytes,
			processName,
			pid,
			cpid,
		)

		ch <- prometheus.MustNewConstMetric(
			c.WorkingSet,
			prometheus.GaugeValue,
			process.WorkingSet,
			processName,
			pid,
			cpid,
		)
	}

	return nil
}
