// init-govdao.gno
package main

import (
	govdaoinit "gno.land/r/gov/dao/v3/init"
)

func main(cur realm) {
	govdaoinit.InitWithUsers(cross(cur), address("g1fz9afdzgqsk2kw6sp44rjthcx25vz0e3ucgst5"))
}
