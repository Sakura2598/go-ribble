package tracetest

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Sakura2598/go-ribble/common"
	"github.com/Sakura2598/go-ribble/common/hexutil"
	"github.com/Sakura2598/go-ribble/core"
	"github.com/Sakura2598/go-ribble/core/rawdb"
	"github.com/Sakura2598/go-ribble/core/types"
	"github.com/Sakura2598/go-ribble/core/vm"
	"github.com/Sakura2598/go-ribble/eth/tracers"
	"github.com/Sakura2598/go-ribble/rlp"
	"github.com/Sakura2598/go-ribble/tests"
)

// flatCallTrace is the result of a callTracerParity run.
type flatCallTrace struct {
	Action              flatCallTraceAction `json:"action"`
	BlockHash           common.Hash         `json:"-"`
	BlockNumber         uint64              `json:"-"`
	Error               string              `json:"error,omitempty"`
	Result              flatCallTraceResult `json:"result,omitempty"`
	Subtraces           int                 `json:"subtraces"`
	TraceAddress        []int               `json:"traceAddress"`
	TransactionHash     common.Hash         `json:"-"`
	TransactionPosition uint64              `json:"-"`
	Type                string              `json:"type"`
	Time                string              `json:"-"`
}

type flatCallTraceAction struct {
	Author         common.Address `json:"author,omitempty"`
	RewardType     string         `json:"rewardType,omitempty"`
	SelfDestructed common.Address `json:"address,omitempty"`
	Balance        hexutil.Big    `json:"balance,omitempty"`
	CallType       string         `json:"callType,omitempty"`
	CreationMethod string         `json:"creationMethod,omitempty"`
	From           common.Address `json:"from,omitempty"`
	Gas            hexutil.Uint64 `json:"gas,omitempty"`
	Init           hexutil.Bytes  `json:"init,omitempty"`
	Input          hexutil.Bytes  `json:"input,omitempty"`
	RefundAddress  common.Address `json:"refundAddress,omitempty"`
	To             common.Address `json:"to,omitempty"`
	Value          hexutil.Big    `json:"value,omitempty"`
}

type flatCallTraceResult struct {
	Address common.Address `json:"address,omitempty"`
	Code    hexutil.Bytes  `json:"code,omitempty"`
	GasUsed hexutil.Uint64 `json:"gasUsed,omitempty"`
	Output  hexutil.Bytes  `json:"output,omitempty"`
}

// flatCallTracerTest defines a single test to check the call tracer against.
type flatCallTracerTest struct {
	Genesis      *core.Genesis   `json:"genesis"`
	Context      *callContext    `json:"context"`
	Input        string          `json:"input"`
	TracerConfig json.RawMessage `json:"tracerConfig"`
	Result       []flatCallTrace `json:"result"`
}

func flatCallTracerTestRunner(tracerName string, filename string, dirPath string, t testing.TB) error {
	// Call tracer test found, read if from disk
	blob, err := os.ReadFile(filepath.Join("testdata", dirPath, filename))
	if err != nil {
		return fmt.Errorf("failed to read testcase: %v", err)
	}
	test := new(flatCallTracerTest)
	if err := json.Unmarshal(blob, test); err != nil {
		return fmt.Errorf("failed to parse testcase: %v", err)
	}
	// Configure a blockchain with the given prestate
	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(common.FromHex(test.Input), tx); err != nil {
		return fmt.Errorf("failed to parse testcase input: %v", err)
	}
	signer := types.MakeSigner(test.Genesis.Config, new(big.Int).SetUint64(uint64(test.Context.Number)), uint64(test.Context.Time))
	context := test.Context.toBlockContext(test.Genesis)
	state := tests.MakePreState(rawdb.NewMemoryDatabase(), test.Genesis.Alloc, false, rawdb.HashScheme)
	defer state.Close()

	// Create the tracer, the EVM environment and run it
	tracer, err := tracers.DefaultDirectory.New(tracerName, new(tracers.Context), test.TracerConfig)
	if err != nil {
		return fmt.Errorf("failed to create call tracer: %v", err)
	}

	state.StateDB.SetLogger(tracer.Hooks)
	msg, err := core.TransactionToMessage(tx, signer, context.BaseFee)
	if err != nil {
		return fmt.Errorf("failed to prepare transaction for tracing: %v", err)
	}
	evm := vm.NewEVM(context, core.NewEVMTxContext(msg), state.StateDB, test.Genesis.Config, vm.Config{Tracer: tracer.Hooks})
	tracer.OnTxStart(evm.GetVMContext(), tx, msg.From)
	vmRet, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(tx.Gas()))
	if err != nil {
		return fmt.Errorf("failed to execute transaction: %v", err)
	}
	tracer.OnTxEnd(&types.Receipt{GasUsed: vmRet.UsedGas}, nil)

	// Retrieve the trace result and compare against the etalon
	res, err := tracer.GetResult()
	if err != nil {
		return fmt.Errorf("failed to retrieve trace result: %v", err)
	}
	ret := make([]flatCallTrace, 0)
	if err := json.Unmarshal(res, &ret); err != nil {
		return fmt.Errorf("failed to unmarshal trace result: %v", err)
	}
	if !jsonEqualFlat(ret, test.Result) {
		t.Logf("test %s failed", filename)

		// uncomment this for easier debugging
		// have, _ := json.MarshalIndent(ret, "", " ")
		// want, _ := json.MarshalIndent(test.Result, "", " ")
		// t.Logf("trace mismatch: \nhave %+v\nwant %+v", string(have), string(want))

		// uncomment this for harder debugging <3 meowsbits
		// lines := deep.Equal(ret, test.Result)
		// for _, l := range lines {
		// 	t.Logf("%s", l)
		// 	t.FailNow()
		// }

		t.Fatalf("trace mismatch: \nhave %+v\nwant %+v", ret, test.Result)
	}
	return nil
}

// Iterates over all the input-output datasets in the tracer parity test harness and
// runs the Native tracer against them.
func TestFlatCallTracerNative(t *testing.T) {
	testFlatCallTracer("flatCallTracer", "call_tracer_flat", t)
}

func testFlatCallTracer(tracerName string, dirPath string, t *testing.T) {
	files, err := os.ReadDir(filepath.Join("testdata", dirPath))
	if err != nil {
		t.Fatalf("failed to retrieve tracer test suite: %v", err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		file := file // capture range variable
		t.Run(camel(strings.TrimSuffix(file.Name(), ".json")), func(t *testing.T) {
			t.Parallel()

			err := flatCallTracerTestRunner(tracerName, file.Name(), dirPath, t)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// jsonEqualFlat is similar to reflect.DeepEqual, but does a 'bounce' via json prior to
// comparison
func jsonEqualFlat(x, y interface{}) bool {
	xTrace := new([]flatCallTrace)
	yTrace := new([]flatCallTrace)
	if xj, err := json.Marshal(x); err == nil {
		json.Unmarshal(xj, xTrace)
	} else {
		return false
	}
	if yj, err := json.Marshal(y); err == nil {
		json.Unmarshal(yj, yTrace)
	} else {
		return false
	}
	return reflect.DeepEqual(xTrace, yTrace)
}

func BenchmarkFlatCallTracer(b *testing.B) {
	files, err := filepath.Glob("testdata/call_tracer_flat/*.json")
	if err != nil {
		b.Fatalf("failed to read testdata: %v", err)
	}

	for _, file := range files {
		filename := strings.TrimPrefix(file, "testdata/call_tracer_flat/")
		b.Run(camel(strings.TrimSuffix(filename, ".json")), func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				err := flatCallTracerTestRunner("flatCallTracer", filename, "call_tracer_flat", b)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
