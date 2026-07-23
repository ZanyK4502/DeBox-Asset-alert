package auth

import (
	"errors"
	"math/big"
)

var (
	secp256k1P  = mustBigInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F")
	secp256k1N  = mustBigInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141")
	secp256k1Gx = mustBigInt("79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798")
	secp256k1Gy = mustBigInt("483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8")
)

type secp256k1Point struct {
	x        *big.Int
	y        *big.Int
	infinity bool
}

func recoverSecp256k1PublicKey(
	hash []byte,
	r *big.Int,
	s *big.Int,
	recoveryID byte,
) (secp256k1Point, error) {
	if recoveryID > 1 ||
		r.Sign() <= 0 || r.Cmp(secp256k1N) >= 0 ||
		s.Sign() <= 0 || s.Cmp(secp256k1N) >= 0 {
		return secp256k1Point{}, errors.New("invalid signature")
	}

	x := new(big.Int).Set(r)
	if x.Cmp(secp256k1P) >= 0 {
		return secp256k1Point{}, errors.New("invalid recovery point")
	}
	alpha := modP(new(big.Int).Add(
		new(big.Int).Exp(x, big.NewInt(3), secp256k1P),
		big.NewInt(7),
	))
	exponent := new(big.Int).Rsh(new(big.Int).Add(secp256k1P, big.NewInt(1)), 2)
	y := new(big.Int).Exp(alpha, exponent, secp256k1P)
	if modP(new(big.Int).Mul(y, y)).Cmp(alpha) != 0 {
		return secp256k1Point{}, errors.New("recovery point is not on the curve")
	}
	if byte(y.Bit(0)) != recoveryID {
		y.Sub(secp256k1P, y)
	}
	recoveryPoint := secp256k1Point{x: x, y: y}
	if !secp256k1ScalarMult(recoveryPoint, secp256k1N).infinity {
		return secp256k1Point{}, errors.New("invalid recovery point order")
	}

	rInverse := new(big.Int).ModInverse(r, secp256k1N)
	if rInverse == nil {
		return secp256k1Point{}, errors.New("signature r is not invertible")
	}
	z := new(big.Int).SetBytes(hash)
	z.Mod(z, secp256k1N)
	leftScalar := new(big.Int).Neg(z)
	leftScalar.Mul(leftScalar, rInverse)
	leftScalar.Mod(leftScalar, secp256k1N)
	rightScalar := new(big.Int).Mul(s, rInverse)
	rightScalar.Mod(rightScalar, secp256k1N)

	publicKey := secp256k1Add(
		secp256k1ScalarBaseMult(leftScalar),
		secp256k1ScalarMult(recoveryPoint, rightScalar),
	)
	if publicKey.infinity || !secp256k1Verify(hash, r, s, publicKey) {
		return secp256k1Point{}, errors.New("could not recover public key")
	}
	return publicKey, nil
}

func secp256k1Verify(hash []byte, r *big.Int, s *big.Int, publicKey secp256k1Point) bool {
	sInverse := new(big.Int).ModInverse(s, secp256k1N)
	if sInverse == nil {
		return false
	}
	z := new(big.Int).SetBytes(hash)
	z.Mod(z, secp256k1N)
	leftScalar := new(big.Int).Mul(z, sInverse)
	leftScalar.Mod(leftScalar, secp256k1N)
	rightScalar := new(big.Int).Mul(r, sInverse)
	rightScalar.Mod(rightScalar, secp256k1N)
	point := secp256k1Add(
		secp256k1ScalarBaseMult(leftScalar),
		secp256k1ScalarMult(publicKey, rightScalar),
	)
	if point.infinity {
		return false
	}
	return new(big.Int).Mod(point.x, secp256k1N).Cmp(r) == 0
}

func secp256k1ScalarBaseMult(scalar *big.Int) secp256k1Point {
	return secp256k1ScalarMult(
		secp256k1Point{
			x: new(big.Int).Set(secp256k1Gx),
			y: new(big.Int).Set(secp256k1Gy),
		},
		scalar,
	)
}

func secp256k1ScalarMult(point secp256k1Point, scalar *big.Int) secp256k1Point {
	result := secp256k1Point{infinity: true}
	addend := cloneSecp256k1Point(point)
	for bit := scalar.BitLen() - 1; bit >= 0; bit-- {
		result = secp256k1Add(result, result)
		if scalar.Bit(bit) == 1 {
			result = secp256k1Add(result, addend)
		}
	}
	return result
}

func secp256k1Add(left secp256k1Point, right secp256k1Point) secp256k1Point {
	if left.infinity {
		return cloneSecp256k1Point(right)
	}
	if right.infinity {
		return cloneSecp256k1Point(left)
	}

	var slope *big.Int
	if left.x.Cmp(right.x) == 0 {
		if modP(new(big.Int).Add(left.y, right.y)).Sign() == 0 {
			return secp256k1Point{infinity: true}
		}
		numerator := new(big.Int).Mul(left.x, left.x)
		numerator.Mul(numerator, big.NewInt(3))
		denominator := new(big.Int).Mul(left.y, big.NewInt(2))
		inverse := new(big.Int).ModInverse(modP(denominator), secp256k1P)
		if inverse == nil {
			return secp256k1Point{infinity: true}
		}
		slope = modP(new(big.Int).Mul(numerator, inverse))
	} else {
		numerator := new(big.Int).Sub(right.y, left.y)
		denominator := new(big.Int).Sub(right.x, left.x)
		inverse := new(big.Int).ModInverse(modP(denominator), secp256k1P)
		if inverse == nil {
			return secp256k1Point{infinity: true}
		}
		slope = modP(new(big.Int).Mul(numerator, inverse))
	}

	x := new(big.Int).Mul(slope, slope)
	x.Sub(x, left.x)
	x.Sub(x, right.x)
	x = modP(x)
	y := new(big.Int).Sub(left.x, x)
	y.Mul(slope, y)
	y.Sub(y, left.y)
	y = modP(y)
	return secp256k1Point{x: x, y: y}
}

func cloneSecp256k1Point(point secp256k1Point) secp256k1Point {
	if point.infinity {
		return secp256k1Point{infinity: true}
	}
	return secp256k1Point{
		x: new(big.Int).Set(point.x),
		y: new(big.Int).Set(point.y),
	}
}

func modP(value *big.Int) *big.Int {
	value.Mod(value, secp256k1P)
	if value.Sign() < 0 {
		value.Add(value, secp256k1P)
	}
	return value
}

func mustBigInt(value string) *big.Int {
	result, ok := new(big.Int).SetString(value, 16)
	if !ok {
		panic("invalid secp256k1 constant")
	}
	return result
}
