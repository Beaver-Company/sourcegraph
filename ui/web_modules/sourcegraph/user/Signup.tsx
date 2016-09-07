// tslint:disable: typedef ordered-imports

import {Location} from "history";
import * as React from "react";
import Helmet from "react-helmet";
import {Link} from "react-router";

import {Container} from "sourcegraph/Container";
import * as Dispatcher from "sourcegraph/Dispatcher";
import {Button, Input, Heading} from "sourcegraph/components";

import * as UserActions from "sourcegraph/user/UserActions";
import {UserStore} from "sourcegraph/user/UserStore";
import {GitHubAuthButton} from "sourcegraph/components/GitHubAuthButton";
import "sourcegraph/user/UserBackend"; // for side effects
import {redirectIfLoggedIn} from "sourcegraph/user/redirectIfLoggedIn";
import * as styles from "sourcegraph/user/styles/accountForm.css";
import {Store} from "sourcegraph/Store";

interface SignupFormProps {
	onSignupSuccess: () => void;
	location: any;

	// returnTo is where the user should be redirected after an OAuth login flow,
	// either a URL path or a Location object.
	returnTo: string | Location;
}

type SignupFormState = any;

export class SignupForm extends Container<SignupFormProps, SignupFormState> {
	_loginInput: any;
	_passwordInput: any;
	_emailInput: any;

	constructor(props: SignupFormProps) {
		super(props);
		this._loginInput = null;
		this._passwordInput = null;
		this._emailInput = null;
		this._handleSubmit = this._handleSubmit.bind(this);
	}

	reconcileState(state, props: SignupFormProps): void {
		Object.assign(state, props);
		state.pendingAuthAction = UserStore.pendingAuthActions["signup"] || false;
		state.authResponse = UserStore.authResponses["signup"] || null;

		// These are set by the GitHub OAuth2 receive endpoint if there is an
		// error.
		state.githubError = (props.location.query && props.location.query["github-signup-error"]) || null;
		state.githubLogin = (props.location.query && props.location.query.login) || null;
		state.githubEmail = (props.location.query && props.location.query.email) || null;
	}

	onStateTransition(prevState: SignupFormState, nextState: SignupFormState): void {
		if (prevState.authResponse !== nextState.authResponse) {
			if (nextState.submitted && nextState.authResponse && nextState.authResponse.Success) {
				setTimeout(() => this.props.onSignupSuccess());
			}
		}
	}

	stores(): Store<any>[] {
		return [UserStore];
	}

	_handleSubmit(ev) {
		ev.preventDefault();
		this.setState({submitted: true}, () => {
			let action = new UserActions.SubmitSignup(
				this._loginInput.value,
				this._passwordInput.value,
				this._emailInput.value,
			);
			Dispatcher.Stores.dispatch(action);
			Dispatcher.Backends.dispatch(action);
		});
	}

	render(): JSX.Element | null {
		return (
			<form {...this.props} onSubmit={this._handleSubmit} className={styles.form}>
				<Heading level="3" align="center" underline="orange">Sign up for Sourcegraph</Heading>
				{!this.state.githubError && [
					<GitHubAuthButton returnTo={this.state.returnTo} tabIndex={1} key="1" block={true}>Continue with GitHub</GitHubAuthButton>,
					<p key="2" className={styles.divider}>or</p>,
				]}
				{this.state.githubError === "username-or-email-taken" && <div className={styles.error}>Your GitHub username <strong>{this.state.githubLogin}</strong> {this.state.githubEmail && <span>or email <strong>{this.state.githubEmail}</strong></span>} is already taken on Sourcegraph. Sign up on Sourcegraph with a different username/email, then link your GitHub account again.</div>}
				{this.state.githubError === "unknown" && <div className={styles.error}>Sorry, signing up via GitHub didn't work. (Check your organization's GitHub 3rd-party application settings.) Try creating a separate Sourcegraph account below.</div>}
				<label>
					<span>Username</span>
					<Input type="text"
						id="e2etest-login-field"
						name="username"
						defaultValue={this.state.githubLogin || null}
						domRef={(e) => this._loginInput = e}
						autoComplete="username"
						autoFocus={true}
						autoCapitalize="off"
						autoCorrect="off"
						minLength={3}
						tabIndex={2}
						block={true}
						required={true} />
				</label>
				<label>
					<span>Email address</span>
					<Input type="email"
						id="e2etest-email-field"
						name="email"
						defaultValue={this.state.githubEmail || null}
						autoComplete="email"
						autoCapitalize="off"
						tabIndex={3}
						domRef={(e) => this._emailInput = e}
						block={true}
						required={true} />
				</label>
				<label>
					<span>Password</span>
					<Input type="password"
						id="e2etest-password-field"
						name="password"
						autoComplete="new-password"
						domRef={(e) => this._passwordInput = e}
						tabIndex={4}
						block={true}
						required={true} />
				</label>
				<p className={styles.mid_text}>
					By creating an account, you agree to our <a href="/-/privacy" target="_blank">privacy policy</a> and <a href="/-/terms" target="_blank">terms</a>.
				</p>
				<Button
					color={this.state.githubError ? "blue" : "normal"}
					id="e2etest-register-button"
					tabIndex="5"
					block={true}
					loading={this.state.submitted && (this.state.pendingAuthAction || (this.state.authResponse && !this.state.authResponse.Error))}>Create account</Button>
				{!this.state.pendingAuthAction && this.state.authResponse && this.state.authResponse.Error &&
					<div className={styles.error}>{this.state.authResponse.Error.body.message}</div>
				}
				<p className={styles.sub_text}>
					Already have an account? <Link tabIndex={6} to="/login">Sign in.</Link>
				</p>
			</form>
		);
	}
}
let StyledSignupForm = SignupForm;

interface SignupProps {
	location: any;
}

function SignupComp(props: SignupProps, {router}) {
	return (
		<div className={styles.full_page}>
			<Helmet title="Sign Up" />
			<StyledSignupForm {...props}
				returnTo="/"
				onSignupSuccess={() => router.replace(Object.assign({}, props.location, {state: Object.assign({}, props.location.state, {_onboarding: "new-user"})}))} />
		</div>
	);
}
(SignupComp as any).contextTypes = {
	router: React.PropTypes.object.isRequired,
};

export const Signup = redirectIfLoggedIn("/", SignupComp);
