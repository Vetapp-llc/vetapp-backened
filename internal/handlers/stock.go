package handlers

import (
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"vetapp-backend/internal/middleware"
	"vetapp-backend/internal/models"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Clinic stock (საწყობი) and selling from it (რეალიზაცია).
//
// A product has a name, the units on hand and a unit price. Selling
// takes units off the product and records an ordinary sale in `shop`
// (name, total price, method, "N ცალი" in the comment), so the sales
// list and the revenue totals see it like any other sale.

const maxStockQuantity = 1_000_000

// ProductResponse is a product in stock.
type ProductResponse struct {
	ID        uint   `json:"id" validate:"required"`
	Name      string `json:"name" validate:"required"`
	Quantity  int    `json:"quantity" validate:"required"`
	UnitPrice string `json:"unitPrice" validate:"required"`
}

func productToResponse(p models.ShopProduct) ProductResponse {
	return ProductResponse{ID: p.ID, Name: p.Name, Quantity: p.Quantity, UnitPrice: trimMoney(p.UnitPrice)}
}

// trimMoney drops the zero cents numeric(12,2) adds: "12.00" → "12".
func trimMoney(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// ProductRequest creates a product, or (as pointers in
// UpdateProductRequest) changes one.
type ProductRequest struct {
	Name      string `json:"name" validate:"required"`
	Quantity  int    `json:"quantity"`
	UnitPrice string `json:"unitPrice" validate:"required"`
}

// UpdateProductRequest is a partial update; omitted fields keep their value.
type UpdateProductRequest struct {
	Name      *string `json:"name"`
	Quantity  *int    `json:"quantity"`
	UnitPrice *string `json:"unitPrice"`
}

func validateProduct(name string, quantity int, unitPrice string) string {
	switch {
	case strings.TrimSpace(name) == "":
		return "name is required"
	case quantity < 0 || quantity > maxStockQuantity:
		return "quantity must be between 0 and 1000000"
	case !isMoney(unitPrice):
		return "unit price must be a number, e.g. 12.50"
	}
	return ""
}

// ListProducts returns the clinic's stock.
// @Summary List stock
// @Tags shop
// @Produce json
// @Security BearerAuth
// @Success 200 {array} ProductResponse
// @Router /shop/products [get]
func (h *ShopHandler) ListProducts(w http.ResponseWriter, r *http.Request) {
	var products []models.ShopProduct
	if err := ownedByCaller(h.db, r, "zip").Order("name ASC, id ASC").Find(&products).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to fetch stock"})
		return
	}
	out := make([]ProductResponse, len(products))
	for i, p := range products {
		out[i] = productToResponse(p)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateProduct adds a product to the clinic's stock.
// @Summary Add product to stock
// @Tags shop
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body ProductRequest true "Product"
// @Success 201 {object} ProductResponse
// @Failure 400 {object} ErrorResponse
// @Router /shop/products [post]
func (h *ShopHandler) CreateProduct(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	var req ProductRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	req.Name, req.UnitPrice = strings.TrimSpace(req.Name), strings.TrimSpace(req.UnitPrice)
	if msg := validateProduct(req.Name, req.Quantity, req.UnitPrice); msg != "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: msg})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "stock belongs to a clinic; this account has no clinic"})
		return
	}
	p := models.ShopProduct{Zip: claims.Zip, Name: req.Name, Quantity: req.Quantity, UnitPrice: req.UnitPrice}
	if err := h.db.Create(&p).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to add product"})
		return
	}
	writeJSON(w, http.StatusCreated, productToResponse(p))
}

// UpdateProduct changes a product's name, units on hand or unit price.
// @Summary Update product in stock
// @Tags shop
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "Product ID"
// @Param body body UpdateProductRequest true "Fields to change"
// @Success 200 {object} ProductResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /shop/products/{id} [put]
func (h *ShopHandler) UpdateProduct(w http.ResponseWriter, r *http.Request) {
	var p models.ShopProduct
	if err := ownedByCaller(h.db, r, "zip").Where("id = ?", chi.URLParam(r, "id")).First(&p).Error; err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "product not found"})
		return
	}
	var req UpdateProductRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if req.Name != nil {
		p.Name = strings.TrimSpace(*req.Name)
	}
	if req.Quantity != nil {
		p.Quantity = *req.Quantity
	}
	if req.UnitPrice != nil {
		p.UnitPrice = strings.TrimSpace(*req.UnitPrice)
	}
	if msg := validateProduct(p.Name, p.Quantity, p.UnitPrice); msg != "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: msg})
		return
	}
	if err := h.db.Model(&p).Select("name", "quantity", "unit_price", "updated_at").Updates(&p).Error; err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update product"})
		return
	}
	writeJSON(w, http.StatusOK, productToResponse(p))
}

// DeleteProduct removes a product. Its past sales stay in the sales list.
// @Summary Delete product from stock
// @Tags shop
// @Produce json
// @Security BearerAuth
// @Param id path int true "Product ID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Router /shop/products/{id} [delete]
func (h *ShopHandler) DeleteProduct(w http.ResponseWriter, r *http.Request) {
	res := ownedByCaller(h.db, r, "zip").Where("id = ?", chi.URLParam(r, "id")).Delete(&models.ShopProduct{})
	if res.Error != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete product"})
		return
	}
	if res.RowsAffected == 0 {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "product not found"})
		return
	}
	writeJSON(w, http.StatusOK, MessageResponse{Message: "product deleted"})
}

// RealizeRequest sells units of a product from stock.
type RealizeRequest struct {
	ProductID uint   `json:"productId" validate:"required"`
	Quantity  int    `json:"quantity" validate:"required,min=1"`
	Date      string `json:"date" validate:"required"`
	Method    string `json:"method" validate:"omitempty,oneof=card cash"`
	Comment   string `json:"comment"`
}

var errOutOfStock = errors.New("out of stock")

// Realize sells units of a product: it takes them off the stock and
// records the sale, both or neither.
// @Summary Sell from stock
// @Tags shop
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body RealizeRequest true "Product, units and payment"
// @Success 201 {object} ShopResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /shop/realize [post]
func (h *ShopHandler) Realize(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	var req RealizeRequest
	if err := decodeAndValidate(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if !isISODate(req.Date) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "date must be YYYY-MM-DD"})
		return
	}
	if claims.Zip == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "sales are recorded by a clinic; this account has no clinic"})
		return
	}

	var sale models.Shop
	err := h.db.Transaction(func(tx *gorm.DB) error {
		var p models.ShopProduct
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND zip = ?", req.ProductID, claims.Zip).First(&p).Error; err != nil {
			return err
		}
		if p.Quantity < req.Quantity {
			return errOutOfStock
		}
		unit, ok := new(big.Rat).SetString(p.UnitPrice)
		if !ok {
			return fmt.Errorf("bad unit price %q", p.UnitPrice)
		}
		total := new(big.Rat).Mul(unit, new(big.Rat).SetInt64(int64(req.Quantity)))
		comment := fmt.Sprintf("%d ცალი", req.Quantity)
		if c := strings.TrimSpace(req.Comment); c != "" {
			comment += " · " + c
		}
		sale = models.Shop{
			Name:    p.Name,
			Price:   trimMoney(total.FloatString(2)),
			Date:    req.Date,
			SK:      claims.Zip,
			Method:  models.NormalizePayMethod(req.Method),
			Comment: comment,
		}
		if err := tx.Create(&sale).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.ShopSaleItem{ShopID: sale.ID, ProductID: p.ID, Quantity: req.Quantity}).Error; err != nil {
			return err
		}
		return tx.Model(&p).Updates(map[string]interface{}{
			"quantity": gorm.Expr("quantity - ?", req.Quantity), "updated_at": gorm.Expr("NOW()"),
		}).Error
	})
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "product not found"})
	case errors.Is(err, errOutOfStock):
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "not enough in stock"})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to record the sale"})
	default:
		resp := shopToResponse(sale)
		resp.ProductID, resp.Quantity = &req.ProductID, req.Quantity
		writeJSON(w, http.StatusCreated, resp)
	}
}
