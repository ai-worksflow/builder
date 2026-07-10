DELETE FROM data_columns AS column_record
WHERE NOT EXISTS (
  SELECT 1 FROM data_tables AS table_record WHERE table_record.id = column_record.table_id
);

ALTER TABLE data_columns
  ADD CONSTRAINT data_columns_table_id_fk
  FOREIGN KEY (table_id) REFERENCES data_tables(id) ON DELETE CASCADE;
