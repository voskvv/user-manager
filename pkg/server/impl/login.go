package impl

import (
	"context"

	"time"

	"fmt"

	"git.containerum.net/ch/grpc-proto-files/auth"
	"git.containerum.net/ch/grpc-proto-files/common"
	umtypes "git.containerum.net/ch/json-types/user-manager"
	cherry "git.containerum.net/ch/kube-client/pkg/cherry/user-manager"
	"git.containerum.net/ch/user-manager/pkg/clients"
	"git.containerum.net/ch/user-manager/pkg/models"
	"git.containerum.net/ch/user-manager/pkg/server"
	"git.containerum.net/ch/user-manager/pkg/utils"
	"github.com/sirupsen/logrus"
)

func (u *serverImpl) BasicLogin(ctx context.Context, request umtypes.LoginRequest) (resp *auth.CreateTokenResponse, err error) {
	u.log.Infoln("Basic login")
	u.log.WithFields(logrus.Fields{
		"username": request.Login,
		"password": request.Password,
	}).Debugln("Basic login details")

	user, err := u.svc.DB.GetUserByLogin(ctx, request.Login)
	if dbErr := u.handleDBError(err); dbErr != nil {
		u.log.WithError(dbErr)
		return resp, cherry.ErrLoginFailed()
	}

	if !utils.CheckPassword(request.Login, request.Password, user.Salt, user.PasswordHash) {
		u.log.WithError(cherry.ErrInvalidLogin())
		return resp, cherry.ErrInvalidLogin()
	}
	if !user.IsActive {
		link, err := u.svc.DB.GetLinkForUser(ctx, umtypes.LinkTypeConfirm, user)
		if err != nil {
			u.log.WithError(err)
			return nil, cherry.ErrInvalidLogin()
		}
		if link == nil {
			err := u.svc.DB.Transactional(ctx, func(ctx context.Context, tx models.DB) error {
				var err error
				link, err = tx.CreateLink(ctx, umtypes.LinkTypeConfirm, 24*time.Hour, user)
				return err
			})
			if err := u.handleDBError(err); err != nil {
				u.log.WithError(err)
				return nil, cherry.ErrInvalidLogin()
			}
		}
		if err := u.checkLinkResendTime(ctx, link); err != nil {
			u.log.WithError(err)
			return nil, err
		}
		go u.linkSend(ctx, link)
		return nil, cherry.ErrNotActivated()
	}
	resp, err = u.createTokens(ctx, user)
	return
}

func (u *serverImpl) OneTimeTokenLogin(ctx context.Context, request umtypes.OneTimeTokenLoginRequest) (*auth.CreateTokenResponse, error) {
	u.log.Info("One-time token login")
	u.log.WithField("token", request.Token).Debug("One-time token login details")
	token, err := u.svc.DB.GetTokenObject(ctx, request.Token)
	if err != nil {
		u.log.WithError(err)
		return nil, cherry.ErrLoginFailed()
	}
	if err := u.loginUserChecks(ctx, token.User); err != nil {
		return nil, err
	}

	var tokens *auth.CreateTokenResponse
	err = u.svc.DB.Transactional(ctx, func(ctx context.Context, tx models.DB) error {
		token.IsActive = false
		token.SessionID = server.MustGetSessionID(ctx)
		if updErr := tx.UpdateToken(ctx, token); updErr != nil {
			return updErr
		}

		var err error
		tokens, err = u.createTokens(ctx, token.User)
		return err
	})
	if err := u.handleDBError(err); err != nil {
		u.log.WithError(err)
		return nil, cherry.ErrLoginFailed()
	}
	return tokens, nil
}

//nolint: gocyclo
func (u *serverImpl) OAuthLogin(ctx context.Context, request umtypes.OAuthLoginRequest) (*auth.CreateTokenResponse, error) {
	u.log.WithFields(logrus.Fields{
		"resource": request.Resource,
	}).Infoln("OAuth login")
	u.log.WithFields(logrus.Fields{
		"resource":        request.Resource,
		"key_to_exchange": request.AccessToken,
	}).Debugln("OAuth login credentials")
	resource, exist := clients.OAuthClientByResource(request.Resource)
	if !exist {
		u.log.WithError(fmt.Errorf(resourceNotSupported, request.Resource))
		return nil, cherry.ErrInvalidLogin().AddDetailsErr(fmt.Errorf(resourceNotSupported, request.Resource))
	}
	info, err := resource.GetUserInfo(ctx, request.AccessToken)
	if err != nil {
		u.log.WithError(err)
		return nil, cherry.ErrUnableBindAccount()
	}
	user, err := u.svc.DB.GetUserByLogin(ctx, info.Email)
	if err := u.handleDBError(err); err != nil {
		u.log.WithError(err)
		return nil, cherry.ErrLoginFailed()
	}
	if err = u.loginUserChecks(ctx, user); err != nil {
		u.log.Info("User is not found by email. Checking bound accounts")
		if info.UserID != "" {
			user, err = u.svc.DB.GetUserByBoundAccount(ctx, request.Resource, info.UserID)
			if err = u.handleDBError(err); err != nil {
				u.log.WithError(err)
				return nil, cherry.ErrLoginFailed()
			}
			if err := u.loginUserChecks(ctx, user); err != nil {
				return nil, err
			}
			return u.createTokens(ctx, user)
		}
		return nil, err
	}

	u.log.Info("User is found by email. Binding account")
	err = u.svc.DB.Transactional(ctx, func(ctx context.Context, tx models.DB) error {
		return tx.BindAccount(ctx, user, request.Resource, info.UserID)
	})
	if err := u.handleDBError(err); err != nil {
		u.log.WithError(err)
		return nil, cherry.ErrLoginFailed()
	}
	return u.createTokens(ctx, user)
}

//nolint: gocyclo
func (u *serverImpl) WebAPILogin(ctx context.Context, request umtypes.LoginRequest) (*umtypes.WebAPILoginResponse, error) {
	u.log.WithField("username", request.Login).Infof("Login through web-api")

	resp, err := u.svc.WebAPIClient.Login(ctx, &request)
	if err != nil {
		u.log.WithError(err)
		return nil, err
	}

	alreadyexist := false
	role := "user"
	user, err := u.svc.DB.GetUserByLogin(ctx, request.Login)
	if err == nil && user != nil {
		if user != nil {
			role = user.Role
			alreadyexist = true
			u.log.Debugf("User %v exists in db and has role: %v", user.Login, role)
		}
	}

	volumes, err := u.svc.WebAPIClient.GetVolumes(ctx, resp.Token, resp.User.ID)
	if err != nil {
		u.log.WithError(err).Warningln("Unable to get volumes")
	}

	namespaces, err := u.svc.WebAPIClient.GetNamespaces(ctx, resp.Token)
	if err != nil {
		u.log.WithError(err).Warningln("Unable to get namespaces")
	}

	tokens, err := u.svc.AuthClient.CreateToken(ctx, &auth.CreateTokenRequest{
		UserAgent:   server.MustGetUserAgent(ctx),
		Fingerprint: server.MustGetFingerprint(ctx),
		UserId:      &common.UUID{Value: resp.User.ID},
		UserIp:      server.MustGetClientIP(ctx),
		UserRole:    role,
		RwAccess:    true,
		Access:      &auth.ResourcesAccess{Volume: volumes, Namespace: namespaces},
		PartTokenId: nil,
	})
	if err != nil {
		u.log.WithError(err)
		return nil, cherry.ErrLoginFailed()
	}

	resp.AccessToken = tokens.AccessToken
	resp.RefreshToken = tokens.RefreshToken

	if !alreadyexist {
		u.log.WithError(err).Warnf("Adding user to new db")
		if _, err = u.CreateUserWebAPI(ctx, resp.User.Login, request.Password, resp.User.ID, resp.User.CreatedAt, resp.User.Data); err != nil {
			u.log.WithError(err).Warnf("Unable to add user to new db")
		}
	}
	return resp, nil
}

func (u *serverImpl) Logout(ctx context.Context) error {
	userID := server.MustGetUserID(ctx)
	tokenID := server.MustGetTokenID(ctx)
	sessionID := server.MustGetSessionID(ctx)
	u.log.WithFields(logrus.Fields{
		"user_id":    userID,
		"token_id":   tokenID,
		"session_id": sessionID,
	}).Info("Logout")

	_, err := u.svc.AuthClient.DeleteToken(ctx, &auth.DeleteTokenRequest{
		UserId:  &common.UUID{Value: userID},
		TokenId: &common.UUID{Value: tokenID},
	})
	if err != nil {
		u.log.WithError(err)
		return cherry.ErrLogoutFailed()
	}

	oneTimeToken, err := u.svc.DB.GetTokenBySessionID(ctx, sessionID)
	if err := u.handleDBError(err); err != nil {
		u.log.WithError(err)
		return cherry.ErrLogoutFailed()
	}
	if oneTimeToken != nil {
		if oneTimeToken.User.ID != userID {
			u.log.WithError(cherry.ErrInvalidLink())
			return cherry.ErrInvalidLink()
		}
		err := u.svc.DB.Transactional(ctx, func(ctx context.Context, tx models.DB) error {
			return u.svc.DB.DeleteToken(ctx, oneTimeToken.Token)
		})
		if err = u.handleDBError(err); err != nil {
			u.log.WithError(err)
			return cherry.ErrInvalidLink()
		}
	}
	return nil
}