package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	databasepkg "lbry/daemon/database"
)

func (rpcServer *RPCServer) handleFileSetStatus(w http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	status, _ := normalized.named["status"].(string)
	if status != "start" && status != "stop" {
		panic(errors.New(`Status must be "start" or "stop".`))
	}
	rows, store := rpcServer.selectManagedFiles(normalized, "status")
	if len(rows) == 0 {
		panic(fmt.Errorf("Unable to find a file for %s", fileMutationFilterRepr(normalized.named, "status")))
	}
	row := rows[0]
	if status == "start" && row.Status != "running" {
		if rpcServer.managedFileController != nil {
			if err := rpcServer.managedFileController.StartManagedFile(normalized.ctx, row); err != nil {
				panic(err)
			}
		} else if _, err := rpcServer.saveManagedFile(normalized.ctx, store, row, nil, nil); err != nil {
			panic(err)
		}
		sendResultResponse(w, "Resumed download")
		return
	}
	if status == "stop" && row.Status == "running" {
		if rpcServer.managedFileController != nil {
			if err := rpcServer.managedFileController.StopManagedFile(normalized.ctx, row); err != nil {
				panic(err)
			}
		} else if err := store.ChangeManagedFileStatus(normalized.ctx, row.StreamHash, "stopped"); err != nil {
			panic(err)
		}
		sendResultResponse(w, "Stopped download")
		return
	}
	if status == "start" {
		sendResultResponse(w, "File was already being downloaded")
	} else {
		sendResultResponse(w, "File was already stopped")
	}
}

func (rpcServer *RPCServer) handleFileSave(w http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	rows, store := rpcServer.selectManagedFiles(normalized, "file_name", "download_directory")
	if len(rows) != 1 {
		sendResultResponse(w, false)
		return
	}
	fileName := fileMutationOptionalString(normalized.named["file_name"])
	downloadDirectory := fileMutationOptionalString(normalized.named["download_directory"])
	updated, err := rpcServer.saveManagedFile(
		normalized.ctx, store, rows[0], fileName, downloadDirectory,
	)
	if err != nil {
		panic(err)
	}
	manager := rpcServer.walletManagerProvider()
	encoded, err := rpcServer.encodeManagedFile(manager.DefaultLedger(), updated, nil)
	if err != nil {
		panic(err)
	}
	sendResultResponse(w, encoded)
}

func (rpcServer *RPCServer) handleFileDelete(w http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	rows, store := rpcServer.selectManagedFiles(
		normalized, "delete_from_download_dir", "delete_all",
	)
	if len(rows) == 0 || (len(rows) > 1 && !transactionListTruthy(normalized.named["delete_all"])) {
		sendResultResponse(w, false)
		return
	}
	deleteFile := transactionListTruthy(normalized.named["delete_from_download_dir"])
	for _, row := range rows {
		deletionController, managesDeletion := rpcServer.managedFileController.(ManagedFileDeletionController)
		if managesDeletion {
			if err := deletionController.PrepareManagedFileDelete(normalized.ctx, row); err != nil {
				panic(err)
			}
		} else if rpcServer.managedFileController != nil {
			if err := rpcServer.managedFileController.StopManagedFile(normalized.ctx, row); err != nil {
				panic(err)
			}
		}
		blobHashes, err := store.DeleteManagedStream(normalized.ctx, row.StreamHash)
		if err != nil {
			if managesDeletion {
				deletionController.FinishManagedFileDelete(row, false)
			}
			panic(err)
		}
		if rpcServer.blobManager != nil {
			if err := rpcServer.blobManager.Delete(blobHashes...); err != nil {
				if managesDeletion {
					deletionController.FinishManagedFileDelete(row, true)
				}
				panic(err)
			}
		}
		if deleteFile && row.FileName != nil && row.DownloadDirectory != nil {
			path := filepath.Join(*row.DownloadDirectory, *row.FileName)
			if _, err := os.Stat(path); err == nil {
				if err := os.Remove(path); err != nil {
					if managesDeletion {
						deletionController.FinishManagedFileDelete(row, true)
					}
					panic(err)
				}
			}
		}
		if managesDeletion {
			deletionController.FinishManagedFileDelete(row, true)
		}
	}
	sendResultResponse(w, true)
}

func (rpcServer *RPCServer) selectManagedFiles(
	normalized normalizedRPCParams, controls ...string,
) ([]databasepkg.ManagedFileRow, ManagedFileStore) {
	store, ok := rpcServer.managedFileLister.(ManagedFileStore)
	if !ok {
		panic(errors.New("managed file store is unavailable"))
	}
	rows, err := store.ListManagedFiles(normalized.ctx)
	if err != nil {
		panic(err)
	}
	controlSet := make(map[string]struct{}, len(controls))
	for _, control := range controls {
		controlSet[control] = struct{}{}
	}
	filters := make(map[string]any)
	for name, value := range normalized.named {
		if _, control := controlSet[name]; control || value == nil {
			continue
		}
		filters[name] = value
	}
	filtered, err := filterManagedFiles(rows, "rowid", "eq", normalizedRPCParams{named: filters})
	if err != nil {
		panic(err)
	}
	return filtered, store
}

func (rpcServer *RPCServer) saveManagedFile(
	ctx context.Context,
	store ManagedFileStore,
	row databasepkg.ManagedFileRow,
	fileName, downloadDirectory *string,
) (databasepkg.ManagedFileRow, error) {
	if rpcServer.managedFileController != nil {
		return rpcServer.managedFileController.SaveManagedFile(
			ctx, row, fileName, downloadDirectory,
		)
	}
	if rpcServer.blobManager == nil {
		return row, errors.New("blob manager is unavailable")
	}
	_, content, err := rpcServer.blobManager.ReadStream(row.SDHash)
	if err != nil {
		return row, err
	}
	directory := ""
	if downloadDirectory != nil {
		directory = *downloadDirectory
	} else if row.DownloadDirectory != nil {
		directory = *row.DownloadDirectory
	} else if value, exists := rpcServer.settings.Get("download_dir"); exists {
		directory, _ = value.(string)
	}
	if directory == "" {
		return row, errors.New("no directory to download to")
	}
	name := ""
	if fileName != nil {
		name = *fileName
	} else if row.FileName != nil {
		name = *row.FileName
	} else {
		name = row.SuggestedFileName
	}
	if name == "" {
		return row, errors.New("no file name to download to")
	}
	if info, statErr := os.Stat(directory); errors.Is(statErr, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o777); err != nil {
			return row, err
		}
	} else if statErr != nil {
		return row, statErr
	} else if !info.IsDir() {
		return row, fmt.Errorf("download directory %q is not a directory", directory)
	}
	name = nextManagedFileName(directory, name)
	if err := store.ChangeManagedFilePath(ctx, row.StreamHash, &name, &directory); err != nil {
		return row, err
	}
	if err := store.ChangeManagedFileStatus(ctx, row.StreamHash, "running"); err != nil {
		return row, err
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, content, 0o666); err != nil {
		_ = os.Remove(path)
		return row, err
	}
	if err := store.SetManagedFileSaved(ctx, row.StreamHash, true); err != nil {
		return row, err
	}
	if err := store.ChangeManagedFileStatus(ctx, row.StreamHash, "finished"); err != nil {
		return row, err
	}
	row.FileName, row.DownloadDirectory = &name, &directory
	row.SavedFile, row.Status = true, "finished"
	return row, nil
}

func nextManagedFileName(directory, fileName string) string {
	fileName = filepath.Base(fileName)
	extension := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, extension)
	candidate := fileName
	for index := 1; ; index++ {
		if info, err := os.Stat(filepath.Join(directory, candidate)); err != nil || info.IsDir() {
			return candidate
		}
		candidate = base + "_" + strconv.Itoa(index) + extension
	}
}

func fileMutationOptionalString(value any) *string {
	if value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		panic(fmt.Errorf("expected string, got %T", value))
	}
	return &text
}

func fileMutationFilterRepr(values map[string]any, controls ...string) string {
	controlSet := make(map[string]struct{}, len(controls))
	for _, control := range controls {
		controlSet[control] = struct{}{}
	}
	parts := make([]string, 0)
	for name, value := range values {
		if _, control := controlSet[name]; control || value == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("'%s': %v", name, value))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
