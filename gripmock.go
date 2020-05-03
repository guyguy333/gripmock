package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/tokopedia/gripmock/stub"
)

func main() {
	outputPointer := flag.String("o", "", "directory to output server.go. Default is $GOPATH/src/grpc/")
	grpcPort := flag.String("grpc-port", "4770", "Port of gRPC tcp server")
	grpcBindAddr := flag.String("grpc-listen", "", "Adress the gRPC server will bind to. Default to localhost, set to 0.0.0.0 to use from another machine")
	adminport := flag.String("admin-port", "4771", "Port of stub admin server")
	adminBindAddr := flag.String("admin-listen", "", "Adress the admin server will bind to. Default to localhost, set to 0.0.0.0 to use from another machine")
	stubPath := flag.String("stub", "", "Path where the stub files are (Optional)")
	imports := flag.String("imports", "/protobuf", "comma separated imports path. default path /protobuf is where gripmock Dockerfile install WKT protos")
	// for backwards compatibility
	if os.Args[1] == "gripmock" {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	flag.Parse()
	fmt.Println("Starting GripMock")

	output := *outputPointer
	if output == "" {
		if os.Getenv("GOPATH") == "" {
			log.Fatal("output is not provided and GOPATH is empty")
		}
		output = os.Getenv("GOPATH") + "/src/grpc"
	}

	// for safety
	output += "/"
	if _, err := os.Stat(output); os.IsNotExist(err) {
		os.Mkdir(output, os.ModePerm)
	}

	// run admin stub server
	stub.RunStubServer(stub.Options{
		StubPath: *stubPath,
		Port:     *adminport,
		BindAddr: *adminBindAddr,
	})

	// parse proto files
	protoPaths := flag.Args()

	if len(protoPaths) == 0 {
		log.Fatal("Need atleast one proto file")
	}

	importDirs := strings.Split(*imports, ",")

	// generate pb.go and grpc server based on proto
	pgpa := generateProtoc(protocParam{
		protoPath:   protoPaths,
		adminPort:   *adminport,
		grpcAddress: *grpcBindAddr,
		grpcPort:    *grpcPort,
		output:      output,
		imports:     importDirs,
	})

	// build the server
	buildServer(output, protoPaths, pgpa)

	// and run
	run, runerr := runGrpcServer(output)

	var term = make(chan os.Signal)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT)
	select {
	case err := <-runerr:
		log.Fatal(err)
	case <-term:
		fmt.Println("Stopping gRPC Server")
		run.Process.Kill()
	}
}

func getProtoFilename(path string) string {
	paths := strings.Split(path, "/")
	return paths[len(paths)-1]
}

func getProtoName(path string) string {
	return strings.Split(getProtoFilename(path), ".")[0]
}

func getGoProtoPath(path string, pgpa map[string]goPackageAlias, output string) string {
	protoname := getProtoName(path)
	if gpa, ok := pgpa[getProtoFilename(path)]; ok {
	    return output + gpa.GoPackage + "/" + protoname + ".pb.go"
	}
	return output + protoname + ".pb.go"
}

type protocParam struct {
	protoPath   []string
	adminPort   string
	grpcAddress string
	grpcPort    string
	output      string
	imports     []string
}

type goPackageAlias struct {
	Alias     string
	GoPackage string
}

func getGoPackageAlias(output string) map[string]goPackageAlias {
	pgpa := make (map[string]goPackageAlias, 1)
	protoGoPackagesFile, err := ioutil.ReadFile(output + "proto.json")
	if err != nil {
		log.Fatal("Unable to open " + output + "proto.json")
	}
	err = json.Unmarshal(protoGoPackagesFile, &pgpa)
	if err != nil {
		log.Fatal("Unable to unmarshal proto.json")
	}
	return pgpa
}

func generateProtoc(param protocParam) map[string]goPackageAlias {
	protodirs := strings.Split(param.protoPath[0], "/")
	protodir := ""
	if len(protodirs) > 0 {
		protodir = strings.Join(protodirs[:len(protodirs)-1], "/") + "/"
	}

	args := []string{"-I", protodir}
	// include well-known-types
	for _, i := range param.imports {
		args = append(args, "-I", i)
	}
	args = append(args, param.protoPath...)
	args = append(args, "--go_out=plugins=grpc:"+param.output)
	args = append(args, fmt.Sprintf("--gripmock_out=admin-port=%s,grpc-address=%s,grpc-port=%s:%s",
		param.adminPort, param.grpcAddress, param.grpcPort, param.output))
	protoc := exec.Command("protoc", args...)
	protoc.Stdout = os.Stdout
	protoc.Stderr = os.Stderr
	err := protoc.Run()
	if err != nil {
		log.Fatal("Fail on protoc ", err)
	}

	pgpa := getGoPackageAlias(param.output)

	// change package to "main" on generated code
	/* for _, proto := range param.protoPath {
		goProtoPath := getGoProtoPath(proto, pgpa, param.output)
		sed := exec.Command("gsed", "-i", `s/^package \w*$/package main/`, goProtoPath)
		sed.Stderr = os.Stderr
		sed.Stdout = os.Stdout
		err = sed.Run()
		if err != nil {
			log.Fatal("Fail on sed")
		}
	}*/
	return pgpa
}

func buildServer(output string, protoPaths []string, pgpa map[string]goPackageAlias) {
	args := []string{"build", "-o", output + "grpcserver", output + "server.go"}
	/* for _, path := range protoPaths {
		goProtoPath := getGoProtoPath(path, pgpa, output)
		output+getProtoName(path)+".pb.go"
		args = append(args, goProtoPath)
	} */
	build := exec.Command("go", args...)
	build.Dir = output
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	err := build.Run()
	if err != nil {
		log.Fatal(err)
	}
}

func runGrpcServer(output string) (*exec.Cmd, <-chan error) {
	run := exec.Command(output + "grpcserver")
	run.Stdout = os.Stdout
	run.Stderr = os.Stderr
	err := run.Start()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("grpc server pid: %d\n", run.Process.Pid)
	runerr := make(chan error)
	go func() {
		runerr <- run.Wait()
	}()
	return run, runerr
}
