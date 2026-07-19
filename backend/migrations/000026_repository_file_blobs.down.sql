DROP TRIGGER IF EXISTS repository_file_blob_immutable ON repository_file_blobs;
DROP FUNCTION IF EXISTS prevent_repository_file_blob_mutation();
DROP TABLE IF EXISTS repository_file_blobs;
