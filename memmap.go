// Copyright © 2014 Steve Francia <spf@spf13.com>.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package afero

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/afero/mem"
)

var mux = &sync.Mutex{}

type MemMapFs struct {
	data  map[string]File
	mutex *sync.RWMutex
}

func (m *MemMapFs) lock() {
	mx := m.getMutex()
	mx.Lock()
}

func (m *MemMapFs) unlock()  { m.getMutex().Unlock() }
func (m *MemMapFs) rlock()   { m.getMutex().RLock() }
func (m *MemMapFs) runlock() { m.getMutex().RUnlock() }

func (m *MemMapFs) getData() map[string]File {
	if m.data == nil {
		m.data = make(map[string]File)

		// Root should always exist, right?
		// TODO: what about windows?
		m.data[FilePathSeparator] = mem.CreateDir(FilePathSeparator)
	}
	return m.data
}

func (m *MemMapFs) getMutex() *sync.RWMutex {
	mux.Lock()
	if m.mutex == nil {
		m.mutex = &sync.RWMutex{}
	}
	mux.Unlock()
	return m.mutex
}
func (MemMapFs) Name() string { return "MemMapFS" }

func (m *MemMapFs) Create(name string) (File, error) {
	name = normalizePath(name)
	m.lock()
	file := mem.CreateFile(name)
	m.getData()[name] = file
	m.registerWithParent(file)
	m.unlock()
	return file, nil
}

func (m *MemMapFs) unRegisterWithParent(fileName string) {
	f, err := m.lockfreeOpen(fileName)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("Unable to unregister \"%v\" with Parent, err: %v", fileName, err)
		}
		return
	}
	parent := m.findParent(f)
	if parent == nil {
		log.Fatal("parent of ", f.Name(), " is nil")
	}
	pmem := parent.(*mem.File)
	fmem := f.(*mem.File)
	mem.RemoveFromMemDir(pmem, fmem)
}

func (m *MemMapFs) findParent(f File) File {
	pdir, _ := filepath.Split(f.Name())
	pdir = filepath.Clean(pdir)
	pfile, err := m.lockfreeOpen(pdir)
	if err != nil {
		return nil
	}
	return pfile
}

func (m *MemMapFs) registerWithParent(f File) {
	if f == nil {
		return
	}
	parent := m.findParent(f)
	if parent == nil {
		pdir := filepath.Dir(filepath.Clean(f.Name()))
		err := m.lockfreeMkdir(pdir, 0777)
		if err != nil {
			//log.Println("Mkdir error:", err)
			return
		}
		parent, err = m.lockfreeOpen(pdir)
		if err != nil {
			//log.Println("Open after Mkdir error:", err)
			return
		}
	}
	pmem := parent.(*mem.File)
	fmem := f.(*mem.File)

	// TODO(mbertschler): memDir is only nil when it was not made with Mkdir
	// or lockfreeMkdir. In this case the parent is also not a real directory.
	// This currently only happens for the file ".".
	// This is a quick hack to make the library usable with relative paths.
	mem.InitializeDir(pmem)
	mem.AddToMemDir(pmem, fmem)
}

func (m *MemMapFs) lockfreeMkdir(name string, perm os.FileMode) error {
	name = normalizePath(name)
	x, ok := m.getData()[name]
	if ok {
		// Only return ErrFileExists if it's a file, not a directory.
		i, err := x.Stat()
		if !i.IsDir() {
			return ErrFileExists
		}
		if err != nil {
			return err
		}
	} else {
		item := mem.CreateDir(name)
		m.getData()[name] = item
		m.registerWithParent(item)
	}
	return nil
}

func (m *MemMapFs) Mkdir(name string, perm os.FileMode) error {
	name = normalizePath(name)

	m.rlock()
	x, ok := m.getData()[name]
	m.runlock()
	if ok {
		// Only return ErrFileExists if it's a file, not a directory.
		i, err := x.Stat()
		if !i.IsDir() {
			return ErrFileExists
		}
		if err != nil {
			return err
		}
	} else {
		m.lock()
		item := mem.CreateDir(name)
		m.getData()[name] = item
		m.registerWithParent(item)
		m.unlock()
	}
	return nil
}

func (m *MemMapFs) MkdirAll(path string, perm os.FileMode) error {
	return m.Mkdir(path, perm)
}

// Handle some relative paths
func normalizePath(path string) string {
	path = filepath.Clean(path)

	switch path {
	case ".":
		return FilePathSeparator
	case "..":
		return FilePathSeparator
	default:
		return path
	}
}

func (m *MemMapFs) Open(name string) (File, error) {
	name = normalizePath(name)

	m.rlock()
	f, ok := m.getData()[name]
	ff, ok := f.(*mem.File)

	if ok {
		ff.Open()
	}
	m.runlock()

	if ok {
		return f, nil
	} else {
		return nil, ErrFileNotFound
	}
}

func (m *MemMapFs) lockfreeOpen(name string) (File, error) {
	name = normalizePath(name)
	f, ok := m.getData()[name]
	ff, ok := f.(*mem.File)
	if ok {
		ff.Open()
	}
	if ok {
		return f, nil
	} else {
		return nil, ErrFileNotFound
	}
}

func (m *MemMapFs) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	file, err := m.Open(name)
	if os.IsNotExist(err) && (flag&os.O_CREATE > 0) {
		file, err = m.Create(name)
	}
	if err != nil {
		return nil, err
	}
	if flag&os.O_APPEND > 0 {
		_, err = file.Seek(0, os.SEEK_END)
		if err != nil {
			file.Close()
			return nil, err
		}
	}
	if flag&os.O_TRUNC > 0 && flag&(os.O_RDWR|os.O_WRONLY) > 0 {
		err = file.Truncate(0)
		if err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}

func (m *MemMapFs) Remove(name string) error {
	name = normalizePath(name)

	m.lock()
	defer m.unlock()

	if _, ok := m.getData()[name]; ok {
		m.unRegisterWithParent(name)
		delete(m.getData(), name)
	} else {
		return &os.PathError{"remove", name, os.ErrNotExist}
	}
	return nil
}

func (m *MemMapFs) RemoveAll(path string) error {
	path = normalizePath(path)
	m.lock()
	m.unRegisterWithParent(path)
	m.unlock()

	m.rlock()
	defer m.runlock()

	for p, _ := range m.getData() {
		if strings.HasPrefix(p, path) {
			m.runlock()
			m.lock()
			delete(m.getData(), p)
			m.unlock()
			m.rlock()
		}
	}
	return nil
}

func (m *MemMapFs) Rename(oldname, newname string) error {
	oldname = normalizePath(oldname)
	newname = normalizePath(newname)

	if oldname == newname {
		return nil
	}

	m.rlock()
	defer m.runlock()
	if _, ok := m.getData()[oldname]; ok {
		if _, ok := m.getData()[newname]; !ok {
			m.runlock()
			m.lock()
			m.unRegisterWithParent(oldname)
			file := m.getData()[oldname].(*mem.File)
			delete(m.getData(), oldname)
			mem.ChangeFileName(file, newname)
			m.getData()[newname] = file
			m.registerWithParent(file)
			m.unlock()
			m.rlock()
		} else {
			return ErrDestinationExists
		}
	} else {
		return ErrFileNotFound
	}
	return nil
}

func (m *MemMapFs) Stat(name string) (os.FileInfo, error) {
	f, err := m.Open(name)
	if err != nil {
		return nil, err
	}
	fi := mem.GetFileInfo(f.(*mem.File))
	return fi, nil
}

func (m *MemMapFs) Chmod(name string, mode os.FileMode) error {
	name = normalizePath(name)
	f, ok := m.getData()[name]
	if !ok {
		return &os.PathError{"chmod", name, ErrFileNotFound}
	}

	ff, ok := f.(*mem.File)
	if ok {
		m.lock()
		mem.SetMode(ff, mode)
		m.unlock()
	} else {
		return errors.New("Unable to Chmod Memory File")
	}
	return nil
}

func (m *MemMapFs) Chtimes(name string, atime time.Time, mtime time.Time) error {
	name = normalizePath(name)
	f, ok := m.getData()[name]
	if !ok {
		return &os.PathError{"chtimes", name, ErrFileNotFound}
	}

	ff, ok := f.(*mem.File)
	if ok {
		m.lock()
		mem.SetModTime(ff, mtime)
		m.unlock()
	} else {
		return errors.New("Unable to Chtime Memory File")
	}
	return nil
}

func (m *MemMapFs) List() {
	for _, x := range m.data {
		y, _ := x.Stat()
		fmt.Println(x.Name(), y.Size())
	}
}

func debugMemMapList(fs Fs) {
	if x, ok := fs.(*MemMapFs); ok {
		x.List()
	}
}
