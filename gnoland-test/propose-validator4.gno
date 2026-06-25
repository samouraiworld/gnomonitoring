// propose-validator4.gno
package main

import (
	"gno.land/r/gov/dao"
	"gno.land/r/gnops/valopers/proposal"
)

func main(cur realm) {
	pr := proposal.NewValidatorProposalRequest(cross(cur), address("g1d7wuvd5qz83clvryls3wqk3ta9rqfqske0zm55"))
	dao.MustCreateProposal(cross(cur), pr)
}
