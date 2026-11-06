package data

import (
	"database/sql"
	"database/sql/driver"

	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
)

// rewritingDriver 在 Prepare 阶段改写 SQL，屏蔽方言差异。
// 只包装 Prepare 即可：database/sql 的 Exec/Query/Tx 最终都会走到 Prepare。
type rewritingDriver struct {
	inner   driver.Driver
	rewrite func(string) string
}

func (d *rewritingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &rewritingConn{Conn: conn, rewrite: d.rewrite}, nil
}

type rewritingConn struct {
	driver.Conn
	rewrite func(string) string
}

func (c *rewritingConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(c.rewrite(query))
}

func init() {
	sql.Register("opidcs-mysql", &rewritingDriver{
		inner:   &mysql.MySQLDriver{},
		rewrite: DialectMySQL.Rewrite,
	})
	sql.Register("opidcs-postgres", &rewritingDriver{
		inner:   &pq.Driver{},
		rewrite: DialectPostgres.Rewrite,
	})
}
