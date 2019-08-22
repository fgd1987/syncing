package receiver

//
import (
	"bufio"
	"container/list"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syncing/comm"
	"syncing/gproto"

	"github.com/golang/protobuf/proto"
)

var (
	step        = 0
	fidPathMap  = make(map[int32]string)
	fidMtimeMap = make(map[int32]int64)
	conn        *comm.Connection
	basePath    string
)

func RunServer() {
	errwriter := bufio.NewWriter(os.Stderr)
	errwriter.WriteString("msg welcome!\n")
	errwriter.Flush()

	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

	conn = comm.NewConn(reader, writer)

	ProcessMsg(conn)

	fmt.Fprintf(os.Stderr, "msg server stoped\n")
	conn.Send(gproto.MSG_B_END, []byte{})
}

func ProcessMsg(conn *comm.Connection) {
	var waitGroup = sync.WaitGroup{}
	i := 0
	for {
		cmd, st, err := conn.Recv()
		if err != nil {
			fmt.Fprintf(os.Stderr, "msg recv err: %s\n", err.Error())
			return
		}

		if cmd == gproto.MSG_A_INITPARAM {
			initParam := st.(*gproto.InitParam)
			step = int(initParam.Step)
			basePath = initParam.BasePath

		} else if cmd == gproto.MSG_A_DIR_INFO {
			ds := st.(*gproto.DirStruct)
			fileSumList, err := FileListCheck(ds)
			if err != nil {
				fmt.Fprintf(os.Stderr, "msg FileListCheck error: %s\n", err.Error())
				return
			}
			fidBytes, err := proto.Marshal(fileSumList)
			if err != nil {
				fmt.Fprintf(os.Stderr, "msg Marsha1 fileSumList error: %s\n", err.Error())
				return
			}

			_, err = conn.Send(gproto.MSG_B_SUMLIST, fidBytes)
			if err != nil {
				fmt.Fprintf(os.Stderr, "msg Send fidBytes  error: %s\n", err.Error())
				return
			}

		} else if cmd == gproto.MSG_A_PATCHLIST {
			patchList := st.(*gproto.PatchList)
			waitGroup.Add(1)
			i++
			go RebuildFile(i, patchList, &waitGroup)

		} else if cmd == gproto.MSG_A_END {
			fmt.Fprintf(os.Stderr, "msg recv end\n")

			break
		} else {
			break
		}
	}
	waitGroup.Wait()
}

func FileListCheck(ds *gproto.DirStruct) (*gproto.FileSumList, error) {
	errwriter := bufio.NewWriter(os.Stderr)
	errwriter.WriteString("msg Unmarshal data success! getname: " + ds.GetName() + "\n")

	var fileSumList gproto.FileSumList

	pathStack := list.New()                        //用于计算全路径
	visitedMap := make(map[*gproto.DirStruct]bool) //记录节点是否访问过

	var waitGroup sync.WaitGroup = sync.WaitGroup{}
	var mutex sync.Mutex = sync.Mutex{}

	stack := list.New()
	stack.PushBack(ds)

	for stack.Back() != nil {
		ds := stack.Back().Value.(*gproto.DirStruct)
		stack.Remove(stack.Back())

		if _, ok := visitedMap[ds]; !ok {
			visitedMap[ds] = true
		}
		pathStack.PushBack(ds)

		fileList := ds.GetFileList()
		var fullPath string
		if len(fileList) > 0 {
			for e := pathStack.Front(); e != nil; e = e.Next() {
				fullPath += ((e.Value.(*gproto.DirStruct).Name) + "/")
			}
		}

		for _, file := range fileList {
			path := basePath + fullPath + file.Name

			fileInfo, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					errwriter.WriteString("msg missing " + path + "\n")
					fidMtimeMap[file.Fid] = file.Mtime
					fidPathMap[file.Fid] = path

					err := os.MkdirAll(filepath.Dir(path), os.ModePerm)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Mkdir err %s %s\n", filepath.Dir(path), err)
						continue
					}
					f, err := os.Create(path) //不存在 先创建
					if err != nil {
						fmt.Fprintf(os.Stderr, "Createfile err %s %s\n", path, err)
						continue
					}
					f.Close()

					var sumList gproto.SumList
					sumList.Fid = file.Fid
					mutex.Lock()
					fileSumList.List = append(fileSumList.List, &sumList)
					mutex.Unlock()
					continue

				} else {
					errwriter.WriteString("msg err " + err.Error() + "\n")
					continue
				}
			}

			if file.Mtime != fileInfo.ModTime().Unix() || file.Size != fileInfo.Size() {
				// errwriter.WriteString("msg diff " + path + "\n")
				fidMtimeMap[file.Fid] = file.Mtime
				fidPathMap[file.Fid] = path

				waitGroup.Add(1)
				go func(fid int32, mypath string) {
					defer waitGroup.Done()

					sumList := MakeSumList(mypath)
					sumList.Fid = fid

					//fmt.Fprintf(os.Stderr, "msg sumList %s  %d\n", path, len(sumList.List))
					mutex.Lock()
					fileSumList.List = append(fileSumList.List, sumList)
					mutex.Unlock()
				}(file.Fid, path)
			}
		}

		//update pathStack
		if len(ds.GetDirList()) == 0 {
			for e := pathStack.Back(); e != nil; {
				item := e.Value.(*gproto.DirStruct)

				childVisited := true
				for _, child := range item.DirList {
					if !visitedMap[child] {
						childVisited = false
						break
					}
				}

				if childVisited {
					preEle := e.Prev()
					pathStack.Remove(e)
					e = preEle
				} else {
					break
				}
			}
		}

		for _, dir := range ds.GetDirList() {
			stack.PushBack(dir)
		}
	}
	errwriter.Flush()

	waitGroup.Wait()
	return &fileSumList, nil
}
