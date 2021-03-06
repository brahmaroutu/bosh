package jobapplier

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bc "bosh/agent/applier/bundlecollection"
	models "bosh/agent/applier/models"
	boshblob "bosh/blobstore"
	bosherr "bosh/errors"
	boshjobsuper "bosh/jobsupervisor"
	boshlog "bosh/logger"
	boshcmd "bosh/platform/commands"
	boshsys "bosh/system"
)

const logTag = "renderedJobApplier"

type renderedJobApplier struct {
	jobsBc        bc.BundleCollection
	blobstore     boshblob.Blobstore
	compressor    boshcmd.Compressor
	fs            boshsys.FileSystem
	jobSupervisor boshjobsuper.JobSupervisor
	logger        boshlog.Logger
}

func NewRenderedJobApplier(
	jobsBc bc.BundleCollection,
	blobstore boshblob.Blobstore,
	compressor boshcmd.Compressor,
	fs boshsys.FileSystem,
	jobSupervisor boshjobsuper.JobSupervisor,
	logger boshlog.Logger,
) *renderedJobApplier {
	return &renderedJobApplier{
		jobsBc:        jobsBc,
		blobstore:     blobstore,
		compressor:    compressor,
		fs:            fs,
		jobSupervisor: jobSupervisor,
		logger:        logger,
	}
}

func (s *renderedJobApplier) Apply(job models.Job) error {
	s.logger.Debug(logTag, "Applying job %v", job)

	jobBundle, err := s.jobsBc.Get(job)
	if err != nil {
		return bosherr.WrapError(err, "Getting job bundle")
	}

	jobInstalled, err := jobBundle.IsInstalled()
	if err != nil {
		return bosherr.WrapError(err, "Checking if job is installed")
	}

	if !jobInstalled {
		err := s.downloadAndInstall(job, jobBundle)
		if err != nil {
			return err
		}
	}

	_, _, err = jobBundle.Enable()
	if err != nil {
		return bosherr.WrapError(err, "Enabling job")
	}

	return nil
}

func (s *renderedJobApplier) downloadAndInstall(job models.Job, jobBundle bc.Bundle) error {
	tmpDir, err := s.fs.TempDir("bosh-agent-applier-jobapplier-RenderedJobApplier-Apply")
	if err != nil {
		return bosherr.WrapError(err, "Getting temp dir")
	}

	defer s.fs.RemoveAll(tmpDir)

	file, err := s.blobstore.Get(job.Source.BlobstoreID, job.Source.Sha1)
	if err != nil {
		return bosherr.WrapError(err, "Getting job source from blobstore")
	}

	defer s.blobstore.CleanUp(file)

	err = s.compressor.DecompressFileToDir(file, tmpDir)
	if err != nil {
		return bosherr.WrapError(err, "Decompressing files to temp dir")
	}

	files, err := s.fs.Glob(filepath.Join(tmpDir, job.Source.PathInArchive, "bin", "*"))
	if err != nil {
		return bosherr.WrapError(err, "Finding job binary files")
	}

	for _, f := range files {
		err = s.fs.Chmod(f, os.FileMode(0755))
		if err != nil {
			return bosherr.WrapError(err, "Making %s executable", f)
		}
	}

	_, _, err = jobBundle.Install(filepath.Join(tmpDir, job.Source.PathInArchive))
	if err != nil {
		return bosherr.WrapError(err, "Installing job bundle")
	}

	return nil
}

func (s *renderedJobApplier) Configure(job models.Job, jobIndex int) (err error) {
	s.logger.Debug(logTag, "Configuring job %v with index %d", job, jobIndex)

	jobBundle, err := s.jobsBc.Get(job)
	if err != nil {
		err = bosherr.WrapError(err, "Getting job bundle")
		return
	}

	fs, jobDir, err := jobBundle.GetInstallPath()
	if err != nil {
		err = bosherr.WrapError(err, "Looking up job directory")
		return
	}

	monitFilePath := filepath.Join(jobDir, "monit")
	if fs.FileExists(monitFilePath) {
		err = s.jobSupervisor.AddJob(job.Name, jobIndex, monitFilePath)
		if err != nil {
			err = bosherr.WrapError(err, "Adding monit configuration")
			return
		}
	}

	monitFilePaths, err := fs.Glob(filepath.Join(jobDir, "*.monit"))
	if err != nil {
		err = bosherr.WrapError(err, "Looking for additional monit files")
		return
	}

	for _, monitFilePath := range monitFilePaths {
		label := strings.Replace(filepath.Base(monitFilePath), ".monit", "", 1)
		subJobName := fmt.Sprintf("%s_%s", job.Name, label)

		err = s.jobSupervisor.AddJob(subJobName, jobIndex, monitFilePath)
		if err != nil {
			err = bosherr.WrapError(err, "Adding additional monit configuration %s", label)
			return
		}
	}

	return nil
}

func (s *renderedJobApplier) KeepOnly(jobs []models.Job) error {
	s.logger.Debug(logTag, "Keeping only jobs %v", jobs)

	installedBundles, err := s.jobsBc.List()
	if err != nil {
		return bosherr.WrapError(err, "Retrieving installed bundles")
	}

	for _, installedBundle := range installedBundles {
		var shouldKeep bool

		for _, job := range jobs {
			jobBundle, err := s.jobsBc.Get(job)
			if err != nil {
				return bosherr.WrapError(err, "Getting job bundle")
			}

			if jobBundle == installedBundle {
				shouldKeep = true
				break
			}
		}

		if !shouldKeep {
			err = installedBundle.Disable()
			if err != nil {
				return bosherr.WrapError(err, "Disabling job bundle")
			}

			// If we uninstall the bundle first, and the disable failed (leaving the symlink),
			// then the next time bundle collection will not include bundle in its list
			// which means that symlink will never be deleted.
			err = installedBundle.Uninstall()
			if err != nil {
				return bosherr.WrapError(err, "Uninstalling job bundle")
			}
		}
	}

	return nil
}
