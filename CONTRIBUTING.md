# Contributing to Truva-G3

Thank you for your interest in contributing to Truva-G3! We welcome contributions from the community and are grateful for your support.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [How to Contribute](#how-to-contribute)
- [Development Setup](#development-setup)
- [Coding Standards](#coding-standards)
- [Testing Guidelines](#testing-guidelines)
- [Submitting Changes](#submitting-changes)
- [Issue Guidelines](#issue-guidelines)

## Code of Conduct

By participating in this project, you agree to abide by our Code of Conduct:
- Be respectful and inclusive
- Welcome newcomers and help them get started
- Focus on constructive criticism
- Accept responsibility for mistakes

## Getting Started

1. Fork the repository on GitHub
2. Clone your fork locally:
   ```bash
   git clone https://github.com/YOUR-USERNAME/truva-g3.git
   cd truva-g3
   ```
3. Add the upstream repository:
   ```bash
   git remote add upstream https://github.com/truvaagents/truva-g3.git
   ```

## How to Contribute

### Reporting Bugs

- Check if the bug has already been reported in [Issues](https://github.com/truvaagents/truva-g3/issues)
- Create a new issue with a clear title and description
- Include:
  - Go version (`go version`)
  - Operating system and version
  - Steps to reproduce the issue
  - Expected behavior vs actual behavior
  - Any relevant error messages or logs

### Suggesting Features

- Check existing issues for similar suggestions
- Open a new issue with the "enhancement" label
- Clearly describe:
  - The problem you're trying to solve
  - Your proposed solution
  - Any alternatives you've considered
  - Potential impact on existing functionality

### Contributing Code

1. **Find an Issue**: Look for issues labeled "good first issue" or "help wanted"
2. **Comment**: Let others know you're working on it
3. **Branch**: Create a feature branch from `main`
4. **Code**: Make your changes following our coding standards
5. **Test**: Add tests for new functionality
6. **Document**: Update documentation as needed
7. **Commit**: Use clear, descriptive commit messages
8. **Push**: Push to your fork
9. **PR**: Create a pull request to `main` branch

## Development Setup

### Prerequisites

- Go 1.22 or higher
- Redis (for testing discovery features)
- Docker (optional, for running services locally)

### Building the Project

```bash
# Download dependencies
go mod download

# Build the framework
go build ./...

# Run tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Run tests with race detection
go test -race ./...
```

### Running Examples

```bash
# Basic agent example
cd examples/basic-agent
go run main.go

# Financial system example (requires Redis)
cd examples/financial-intelligence-system
docker-compose up -d redis
./deploy.sh
```

## Coding Standards

### Go Code Style

- Follow the official [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- Use `gofmt` to format code
- Use `golint` and `go vet` for linting
- Keep functions small and focused
- Write clear, self-documenting code
- Add comments for exported types and functions

### Package Organization

```
pkg/
├── <package>/
│   ├── interfaces.go     # Interface definitions
│   ├── doc.go            # Package documentation
│   ├── <impl>.go         # Implementation files
│   └── <impl>_test.go    # Test files
```

### Error Handling

- Always check and handle errors
- Wrap errors with context using `fmt.Errorf`
- Use custom error types when appropriate
- Log errors at the appropriate level

### Naming Conventions

- Use descriptive, meaningful names
- Follow Go naming conventions:
  - Exported identifiers start with capital letters
  - Unexported identifiers start with lowercase
  - Acronyms should be all caps (e.g., `HTTPServer`)
  - Interfaces typically end with "-er" suffix

## Testing Guidelines

### Test Coverage

- Aim for at least 80% test coverage
- Write unit tests for all new functionality
- Include both positive and negative test cases
- Test edge cases and error conditions

### Test Structure

```go
func TestFunctionName(t *testing.T) {
    tests := []struct {
        name    string
        input   InputType
        want    OutputType
        wantErr bool
    }{
        {
            name:  "successful case",
            input: validInput,
            want:  expectedOutput,
        },
        {
            name:    "error case",
            input:   invalidInput,
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := FunctionToTest(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("FunctionToTest() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("FunctionToTest() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Integration Tests

- Place integration tests in `test/` directory
- Use build tags for integration tests: `// +build integration`
- Mock external dependencies when possible
- Use Docker containers for testing with real services

## Submitting Changes

### Commit Messages

Follow the conventional commit format:
```
<type>(<scope>): <subject>

<body>

<footer>
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `style`: Code style changes (formatting, etc.)
- `refactor`: Code refactoring
- `test`: Test additions or changes
- `chore`: Build process or auxiliary tool changes

Example:
```
feat(discovery): add kubernetes service discovery

Implement service discovery using Kubernetes API to automatically
register and discover agents within a cluster.

Closes #123
```

### Pull Request Process

1. **Update Documentation**: Include relevant documentation updates
2. **Pass CI**: Ensure all CI checks pass
3. **Review Ready**: Mark PR as ready for review
4. **Address Feedback**: Respond to review comments promptly
5. **Keep Updated**: Rebase on main if needed
6. **Clean History**: Squash commits if requested

### Pull Request Template

```markdown
## Description
Brief description of changes

## Type of Change
- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Documentation update

## Testing
- [ ] Unit tests pass
- [ ] Integration tests pass
- [ ] Manual testing completed

## Checklist
- [ ] Code follows project style guidelines
- [ ] Self-review completed
- [ ] Documentation updated
- [ ] Tests added/updated
- [ ] Breaking changes documented
```

## Issue Guidelines

### Issue Templates

Use appropriate issue templates when available:
- Bug Report
- Feature Request
- Documentation Issue
- Question

### Labels

Common labels used in the project:
- `good first issue`: Good for newcomers
- `help wanted`: Community help needed
- `bug`: Something isn't working
- `enhancement`: New feature or request
- `documentation`: Documentation improvements
- `duplicate`: Duplicate issue
- `wontfix`: Will not be worked on

## Getting Help

- Read the [documentation](https://github.com/truvaagents/truva-g3/tree/main/docs)
- Check [existing issues](https://github.com/truvaagents/truva-g3/issues)
- Join community discussions
- Ask questions in issues with the "question" label

## Recognition

Contributors will be recognized in:
- The project README
- Release notes
- Special thanks in documentation

Thank you for contributing to Truva-G3!